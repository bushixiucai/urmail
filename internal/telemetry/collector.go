// Package telemetry persists Postfix event batches and checkpoints atomically.
package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/db"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

// ErrCheckpointMoved means another collector committed this stream first.
var ErrCheckpointMoved = errors.New("collector checkpoint advanced concurrently")

var (
	collectorPolls     = promauto.NewCounterVec(prometheus.CounterOpts{Name: "urmail_collector_polls_total", Help: "Bounded collector poll outcomes."}, []string{"result"})
	collectorEvents    = promauto.NewCounterVec(prometheus.CounterOpts{Name: "urmail_delivery_events_total", Help: "Persisted source events by delivery outcome."}, []string{"status"})
	collectorGaps      = promauto.NewCounterVec(prometheus.CounterOpts{Name: "urmail_collector_gaps_total", Help: "Detected mail log source discontinuities."}, []string{"reason"})
	collectorFreshness = promauto.NewGauge(prometheus.GaugeOpts{Name: "urmail_collector_last_success_timestamp_seconds", Help: "Last successfully committed mail log observation."})
)

// Source returns a complete-line batch for a trusted source cursor.
type Source interface {
	Read(context.Context, string, int64) (agentd.MailLogBatch, error)
}

// AgentSource delegates fixed log access to the privileged typed agent.
type AgentSource struct{ Client *agent.Client }

// Read returns only the fixed agent log source; it cannot select a path.
func (s AgentSource) Read(ctx context.Context, identity string, offset int64) (agentd.MailLogBatch, error) {
	var batch agentd.MailLogBatch
	if s.Client == nil {
		return batch, errors.New("typed log agent is not configured")
	}
	response, err := s.Client.Do(ctx, agentd.OpMailLogRead, agentd.MailLogReadParams{Identity: identity, Offset: offset})
	if err != nil {
		return batch, err
	}
	if err = json.Unmarshal([]byte(response.Stdout), &batch); err != nil {
		return batch, errors.New("invalid typed mail log response")
	}
	return batch, nil
}

// Collector correlates a single configured Postfix node's source stream.
type Collector struct {
	Pool   *db.Pool
	Source Source
	Stream string
}

// Poll reads outside a transaction, then compares and atomically advances the
// checkpoint with every event and its durable queue correlation context.
func (c *Collector) Poll(ctx context.Context) error {
	if c.Pool == nil || c.Source == nil || c.Stream == "" || len(c.Stream) > 255 {
		return errors.New("collector requires a pool, fixed source and bounded stream identity")
	}
	expected, err := store.New(c.Pool.Pool).PlatformGetCollectorCheckpoint(ctx, c.Stream)
	if errors.Is(err, pgx.ErrNoRows) {
		expected = store.CollectorCheckpoints{Stream: c.Stream}
	} else if err != nil {
		return err
	}
	batch, err := c.Source.Read(ctx, expected.SourceIdentity, expected.ByteOffset)
	if err != nil {
		return err
	}
	return c.Apply(ctx, expected, batch)
}

// Apply commits one bounded source generation. Source event identity does not
// depend on a guessed tenant or on the parser's reference wall clock.
func (c *Collector) Apply(ctx context.Context, expected store.CollectorCheckpoints, batch agentd.MailLogBatch) error {
	if c.Pool == nil || c.Stream == "" {
		return errors.New("collector state unavailable")
	}
	if batch.Reset && batch.StartOffset != 0 {
		return errors.New("new source generation must begin at zero")
	}
	if batch.GapReason != "" && !batch.Reset {
		return errors.New("source gap requires a new generation")
	}
	if batch.Identity == "" || len(batch.Identity) > 160 || len(batch.Data) > 256<<10 || batch.StartOffset < 0 || batch.EndOffset-batch.StartOffset != int64(len(batch.Data)) || (batch.Data != "" && !strings.HasSuffix(batch.Data, "\n")) || batch.ObservedAt.IsZero() {
		return errors.New("invalid complete-line telemetry batch")
	}
	switch batch.GapReason {
	case "", "rotation_generation_missing", "source_truncated":
	default:
		return errors.New("unknown source gap classification")
	}
	if !batch.Reset && (batch.Identity != expected.SourceIdentity || batch.StartOffset != expected.ByteOffset) {
		return errors.New("source cursor changed without a generation boundary")
	}
	tx, err := c.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = q.PlatformEnsureCollectorCheckpoint(ctx, c.Stream); err != nil {
		return err
	}
	current, err := q.PlatformLockCollectorCheckpoint(ctx, c.Stream)
	if err != nil {
		return err
	}
	if current.SourceIdentity != expected.SourceIdentity || current.Generation != expected.Generation || current.ByteOffset != expected.ByteOffset {
		return ErrCheckpointMoved
	}
	generation := expected.Generation
	if batch.Reset {
		generation++
	}
	if batch.GapReason != "" {
		if err = q.PlatformResetDeliveryQueueContexts(ctx, c.Stream); err != nil {
			return err
		}
	}
	offset := batch.StartOffset
	statuses := map[string]int{}
	for _, line := range strings.SplitAfter(batch.Data, "\n") {
		if line == "" {
			continue
		}
		identity := sourceEventID(c.Stream, batch.Identity, generation, offset)
		offset += int64(len(line))
		event, parseErr := domainMail.ParsePostfixLogLineWithOptions(strings.TrimSuffix(line, "\n"), domainMail.ParserOptions{Now: batch.ObservedAt, Location: time.Local})
		if errors.Is(parseErr, domainMail.ErrNotPostfixLine) {
			continue
		}
		if parseErr != nil {
			return errors.New("postfix source event could not be parsed")
		}
		instance := c.Stream + "/" + event.Process
		state, stateErr := q.PlatformGetDeliveryQueueContext(ctx, store.PlatformGetDeliveryQueueContextParams{Stream: c.Stream, Instance: instance, QueueID: event.QueueID})
		if stateErr != nil && !errors.Is(stateErr, pgx.ErrNoRows) {
			return stateErr
		}
		if errors.Is(stateErr, pgx.ErrNoRows) || event.SMTPClientRecord || event.QueueID == "NOQUEUE" || state.Closed || (event.MessageID != "" && state.MessageID != "" && event.MessageID != state.MessageID) {
			state = store.DeliveryQueueContexts{Stream: c.Stream, Instance: instance, QueueID: event.QueueID, LifecycleID: identity, StartedAt: pgtype.Timestamptz{Time: event.Timestamp, Valid: true}}
		}
		if event.MessageID != "" {
			state.MessageID = event.MessageID
		}
		if event.Sender != "" {
			state.Sender = event.Sender
		}
		if event.AuthenticatedMailbox != "" {
			state.AuthenticatedMailbox = event.AuthenticatedMailbox
		}

		authenticatedTenant, authErr := canonicalTenant(ctx, q, state.AuthenticatedMailbox)
		if authErr != nil {
			return authErr
		}
		localTenant, localErr := canonicalTenant(ctx, q, event.Recipient)
		if localErr != nil {
			return localErr
		}
		tenant := authenticatedTenant
		if !tenant.Valid || event.Component == "lmtp" || event.Component == "local" || event.Component == "virtual" {
			tenant = localTenant
		}
		if authenticatedTenant.Valid {
			if err = q.PlatformAttributeAuthenticatedDeliveryLifecycle(ctx, store.PlatformAttributeAuthenticatedDeliveryLifecycleParams{Instance: instance, LifecycleID: state.LifecycleID, TenantID: authenticatedTenant}); err != nil {
				return err
			}
		}
		state.Closed = event.Status == domainMail.DeliveryStatusRemoved || event.Status == domainMail.DeliveryStatusExpired
		if err = q.PlatformPutDeliveryQueueContext(ctx, store.PlatformPutDeliveryQueueContextParams{Stream: c.Stream, Instance: instance, QueueID: event.QueueID, LifecycleID: state.LifecycleID, MessageID: state.MessageID, AuthenticatedMailbox: state.AuthenticatedMailbox, Sender: state.Sender, StartedAt: state.StartedAt, Closed: state.Closed}); err != nil {
			return err
		}
		sender := event.Sender
		if sender == "" {
			sender = state.Sender
		}
		senderDomain := ""
		if at := strings.LastIndexByte(sender, '@'); at >= 0 {
			senderDomain = sender[at+1:]
		}
		inserted, insertErr := q.PlatformRecordCollectedDeliveryEvent(ctx, store.PlatformRecordCollectedDeliveryEventParams{TenantID: tenant, Instance: instance, QueueID: event.QueueID, MessageID: text(state.MessageID), Sender: sender, SenderDomain: senderDomain, Recipient: text(event.Recipient), RecipientDomain: text(event.RecipientDomain), Relay: text(event.Relay), Status: event.Status, Dsn: text(event.DSN), DelaySeconds: event.DelaySeconds, ResponseDetail: text(bounded(event.ResponseDetail, 2048)), OccurredAt: pgtype.Timestamptz{Time: event.Timestamp, Valid: true}, SourceEventID: identity, LifecycleID: state.LifecycleID})
		if insertErr != nil {
			return insertErr
		}
		if inserted > 0 {
			statuses[event.Status]++
		}
	}
	observation := pgtype.Timestamptz{Time: batch.ObservedAt, Valid: !batch.MoreAvailable}
	if err = q.PlatformAdvanceCollectorCheckpoint(ctx, store.PlatformAdvanceCollectorCheckpointParams{Stream: c.Stream, SourceIdentity: batch.Identity, Generation: generation, ByteOffset: batch.EndOffset, GapReason: text(batch.GapReason), ObservedAt: observation}); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	for status, count := range statuses {
		collectorEvents.WithLabelValues(status).Add(float64(count))
	}
	if batch.GapReason != "" {
		collectorGaps.WithLabelValues(batch.GapReason).Inc()
	}
	if batch.MoreAvailable {
		collectorFreshness.Set(0)
	} else {
		collectorFreshness.Set(float64(batch.ObservedAt.Unix()))
	}
	return nil
}

func canonicalTenant(ctx context.Context, q *store.Queries, address string) (pgtype.Int8, error) {
	if address == "" {
		return pgtype.Int8{}, nil
	}
	tenant, err := q.PlatformResolveDeliveryMailboxTenant(ctx, strings.ToLower(address))
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.Int8{}, nil
	}
	if err != nil {
		return pgtype.Int8{}, err
	}
	return pgtype.Int8{Int64: tenant, Valid: true}, nil
}
func sourceEventID(stream, identity string, generation, offset int64) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d", stream, identity, generation, offset)))
	return hex.EncodeToString(hash[:])
}
func text(value string) pgtype.Text { return pgtype.Text{String: value, Valid: value != ""} }
func bounded(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

// Start polls a configured trusted source until shutdown. Collection errors
// preserve the old cursor, remain visible in metrics, and never mean empty.
func (c *Collector) Start(ctx context.Context, logger *zap.Logger) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := c.Poll(ctx); err != nil {
			if errors.Is(err, ErrCheckpointMoved) {
				collectorPolls.WithLabelValues("concurrent").Inc()
			} else if ctx.Err() == nil {
				collectorPolls.WithLabelValues("error").Inc()
				logger.Warn("mail telemetry collection failed", zap.Error(err))
			}
		} else {
			collectorPolls.WithLabelValues("success").Inc()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
