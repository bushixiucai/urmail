package marketing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

// SecretBox reuses the platform's existing encrypted secret format.
type SecretBox interface {
	EncryptString(string) (string, error)
	DecryptString(string) (string, error)
}

var (
	// ErrDisabled prevents any work before explicit activation outside development/quarantine.
	ErrDisabled = errors.New("marketing worker is disabled")
	// ErrStale rejects the result of an expired or superseded attempt.
	ErrStale = errors.New("marketing attempt lost its lease")
)

// Options enables egress explicitly. DevMode and Quarantined override Enabled.
type Options struct {
	Pool           *db.Pool
	Secrets        SecretBox
	Tokens         *domainMail.SuppressionManager
	Enabled        bool
	DevMode        bool
	Quarantined    bool
	Hostname       string
	UnsubscribeURL string
	Logger         *zap.Logger
	Sender         Sender
}

// terminalSkips are authorization outcomes that can never recover within the
// grace window; the recipient is skipped with its recorded reason.
var terminalSkips = map[string]bool{"entitlement_denied": true, "quota_unconfigured": true, "channel_unavailable": true, "sender_unavailable": true, "timezone_invalid": true}

// Worker owns one process identity; the database owns every durable attempt.
type Worker struct {
	opts Options
	id   string
}

// NewWorker validates active egress without forcing disabled deployments online.
func NewWorker(opts Options) (*Worker, error) {
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.Sender == nil {
		opts.Sender = SendSMTP
	}
	if opts.Enabled && !opts.DevMode && !opts.Quarantined {
		u, err := url.Parse(opts.UnsubscribeURL)
		if opts.Pool == nil || opts.Secrets == nil || opts.Tokens == nil || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || opts.Hostname == "" || strings.ContainsAny(opts.Hostname, "\r\n /\\@") {
			return nil, errors.New("marketing egress configuration is incomplete")
		}
	}
	return &Worker{opts: opts, id: uuid.NewString()}, nil
}

// Run polls durable work; it logs safe classifications, never message content.
func (w *Worker) Run(ctx context.Context) {
	if !w.opts.Enabled || w.opts.DevMode || w.opts.Quarantined {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := w.Step(ctx); err != nil && !errors.Is(err, context.Canceled) {
			w.opts.Logger.Warn("marketing worker requires attention")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Step claims and processes at most one recipient, without long network transactions.
func (w *Worker) Step(ctx context.Context) (bool, error) {
	if !w.opts.Enabled || w.opts.DevMode || w.opts.Quarantined {
		return false, ErrDisabled
	}
	q := store.New(w.opts.Pool)
	count, err := q.PlatformRecoverMarketingSubmissions(ctx)
	if err != nil {
		return false, err
	}
	recoveries.WithLabelValues("uncertain").Add(float64(count))
	count, err = q.PlatformRecoverMarketingClaims(ctx)
	if err != nil {
		return false, err
	}
	recoveries.WithLabelValues("queued").Add(float64(count))

	if _, err := q.PlatformCompleteMarketingCampaigns(ctx); err != nil {
		return false, err
	}
	lastScan.SetToCurrentTime()
	recipient, err := q.PlatformClaimMarketingRecipient(ctx, w.id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	data, err := q.PlatformGetMarketingDelivery(ctx, store.PlatformGetMarketingDeliveryParams{TenantID: recipient.TenantID, ID: recipient.ID})
	if err != nil {
		return false, err
	}
	if !strings.HasPrefix(data.EncryptedConfig, "enc:v1:") {
		return false, errors.New("marketing channel encryption is required")
	}
	plain, err := w.opts.Secrets.DecryptString(data.EncryptedConfig)
	if err != nil {
		return false, errors.New("marketing channel cannot be decrypted")
	}
	var cfg SMTPConfig
	if json.Unmarshal([]byte(plain), &cfg) != nil || cfg.Validate(data.Kind) != nil {
		return false, ErrChannelUnavailable
	}
	outcome, err := q.PlatformAuthorizeMarketingSend(ctx, store.PlatformAuthorizeMarketingSendParams{TenantID: recipient.TenantID, RecipientID: recipient.ID, LeaseOwner: w.id, Fence: recipient.Fence})
	if err != nil {
		return false, err
	}
	if outcome != "authorized" {
		// Permanent execution-time precondition failures end the recipient
		// forever with a recorded reason: the overdue policy forbids marketing
		// from lingering after entitlement, plan or channel gates close.
		if terminalSkips[outcome] {
			if _, err = q.PlatformSkipMarketingRecipient(ctx, store.PlatformSkipMarketingRecipientParams{TenantID: recipient.TenantID, RecipientID: recipient.ID, LeaseOwner: w.id, Fence: recipient.Fence, Reason: outcome}); err != nil {
				return false, err
			}
			revocations.WithLabelValues(outcome).Inc()
			return true, nil
		}
		if outcome != "suppressed" && outcome != "stale" {
			if _, relErr := q.PlatformReleaseMarketingClaim(ctx, store.PlatformReleaseMarketingClaimParams{TenantID: recipient.TenantID, ID: recipient.ID, LeaseOwner: pgtype.Text{String: w.id, Valid: true}, Fence: recipient.Fence}); relErr != nil {
				// The periodic PlatformRecoverMarketingClaims pass still requeues
				// the abandoned claim, so surfacing this failure ends the poll.
				w.opts.Logger.Warn("marketing claim release failed", zap.Error(relErr))
			}
		}
		return false, nil
	}
	u, _ := url.Parse(w.opts.UnsubscribeURL)
	query := u.Query()
	query.Set("token", w.opts.Tokens.GenerateUnsubscribeToken(recipient.TenantID, recipient.Email))
	u.RawQuery = query.Encode()
	delivery := Delivery{ID: recipient.ID, TenantID: recipient.TenantID, CampaignID: recipient.CampaignID, Email: recipient.Email, Subject: data.SubjectSnapshot.String, Body: data.BodySnapshot.String, MessageID: fmt.Sprintf("marketing-%d-%d@%s", recipient.CampaignID, recipient.ID, w.opts.Hostname), EnvelopeFrom: cfg.BounceAddress, UnsubscribeURL: u.String()}
	delivery.BeforeData = func(checkCtx context.Context) error {
		allowed, checkErr := q.PlatformMarketingAttemptStillAllowed(checkCtx, store.PlatformMarketingAttemptStillAllowedParams{TenantID: recipient.TenantID, RecipientID: recipient.ID, LeaseOwner: w.id, Fence: recipient.Fence})
		if checkErr != nil {
			return errors.New("marketing authorization recheck unavailable")
		}
		if !allowed {
			return ErrStale
		}
		return nil
	}
	sendErr := w.opts.Sender(ctx, cfg, delivery)
	// A revocation that landed between claim and DATA terminates this
	// recipient immediately: refund the reservation, record the reason, and
	// never retry. This is the unsubscribe linearization point.
	if sendErr != nil && errors.Is(sendErr, ErrStale) {
		finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		revoked, revokeErr := q.PlatformMarkMarketingRecipientRevoked(finishCtx, store.PlatformMarkMarketingRecipientRevokedParams{TenantID: recipient.TenantID, RecipientID: recipient.ID, LeaseOwner: w.id, Fence: recipient.Fence})
		if revokeErr != nil {
			return false, revokeErr
		}
		if !revoked {
			return false, ErrStale
		}
		revocations.WithLabelValues("unsubscribed_mid_run").Inc()
		return true, nil
	}
	result := "sent"
	if sendErr != nil {
		result = "failed"
		if domainMail.IsDeliveryUncertain(sendErr) {
			result = "uncertain"
		} else if errors.Is(sendErr, ErrPermanentBounce) {
			result = "bounced"
		}
	}
	// Cancellation after SMTP cannot erase the delivery fact. A bounded
	// detached write is safe; a crash/failure remains submitting -> uncertain.
	finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	finished, err := q.PlatformFinishMarketingSend(finishCtx, store.PlatformFinishMarketingSendParams{TenantID: recipient.TenantID, RecipientID: recipient.ID, LeaseOwner: w.id, Fence: recipient.Fence, Result: result})
	if err != nil {
		return false, err
	}
	if !finished {
		return false, ErrStale
	}
	attempts.WithLabelValues(result).Inc()
	if result == "uncertain" {
		return true, domainMail.ErrDeliveryUncertain
	}
	return true, nil
}
