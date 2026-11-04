package marketing

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
	graph "github.com/bushixiucai/urmail/internal/domain/marketingflow"
	"github.com/bushixiucai/urmail/internal/marketingflow"
	"github.com/bushixiucai/urmail/internal/store"
)

// FlowOptions gate and configure the durable marketing flow tick worker.
// Enabled, DevMode and Quarantined carry the same meaning as the campaign
// worker's options; DevMode and Quarantined override Enabled.
type FlowOptions struct {
	Pool        *db.Pool
	Sender      marketingflow.Sender
	Enabled     bool
	DevMode     bool
	Quarantined bool
	Logger      *zap.Logger
	// Lease is the claim lease duration; a crashed worker's positions are
	// recovered after it expires. Zero means two minutes.
	Lease time.Duration
	// Batch bounds how many due positions one tick claims. Zero means fifty.
	Batch int
}

// FlowWorker drives marketingflow.Advance against the durable positions. The
// claim lease plus the SECURITY DEFINER sendable/commit functions are the
// linearization: two concurrent ticks can never send one step twice.
type FlowWorker struct {
	opts  FlowOptions
	id    string
	store *FlowStore
}

// NewFlowWorker validates the flow worker configuration.
func NewFlowWorker(opts FlowOptions) (*FlowWorker, error) {
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.Pool == nil {
		return nil, errors.New("flow worker requires a database pool")
	}
	if opts.Lease <= 0 {
		opts.Lease = 2 * time.Minute
	}
	if opts.Batch <= 0 {
		opts.Batch = 50
	}
	return &FlowWorker{opts: opts, id: uuid.NewString(), store: &FlowStore{Pool: opts.Pool}}, nil
}

// Run polls until the context is done; disabled deployments stay idle.
func (w *FlowWorker) Run(ctx context.Context) {
	if !w.opts.Enabled || w.opts.DevMode || w.opts.Quarantined {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := w.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
			w.opts.Logger.Warn("marketing flow worker requires attention")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick recovers expired claims, then claims and advances one bounded batch of
// due positions. It returns how many positions were processed.
func (w *FlowWorker) Tick(ctx context.Context) (int, error) {
	if !w.opts.Enabled || w.opts.DevMode || w.opts.Quarantined {
		return 0, ErrDisabled
	}
	q := store.New(w.opts.Pool)
	leases, err := q.PlatformRecoverMarketingFlowLeases(ctx)
	if err != nil {
		return 0, err
	}
	if leases > 0 {
		flowRecoveries.WithLabelValues("lease").Add(float64(leases))
	}
	uncertain, err := q.PlatformRecoverMarketingFlowUncertain(ctx)
	if err != nil {
		return 0, err
	}
	if uncertain > 0 {
		flowRecoveries.WithLabelValues("uncertain").Add(float64(uncertain))
	}
	claimed, err := q.PlatformClaimMarketingFlowPositions(ctx, store.PlatformClaimMarketingFlowPositionsParams{
		// Clamp to the SQL int4 domain: the operator-facing options are
		// plain ints, and a value beyond int32 would otherwise wrap.
		Batch:        clampInt32(int64(w.opts.Batch)),
		LeaseOwner:   w.id,
		LeaseSeconds: clampInt32(int64(w.opts.Lease / time.Second)),
	})
	if err != nil {
		return 0, err
	}
	flowAttempts.WithLabelValues("claimed").Add(float64(len(claimed)))
	flowLastScan.SetToCurrentTime()
	for _, position := range claimed {
		w.handle(ctx, q, position)
	}
	return len(claimed), nil
}

// handle advances one claimed position. Every path releases the claim lease,
// guarded by owner and fence so a recovered position is never released by a
// stale worker.
func (w *FlowWorker) handle(ctx context.Context, q *store.Queries, p store.PlatformClaimMarketingFlowPositionsRow) {
	lease := &flowLease{owner: w.id, fence: p.LeaseFence}
	ctx = context.WithValue(ctx, flowLeaseKey{}, lease)
	if p.LeaseUntil.Valid {
		bounded, cancel := context.WithDeadline(ctx, p.LeaseUntil.Time.Add(-5*time.Second))
		defer cancel()
		ctx = bounded
	}
	status, code, retry := "blocked", "", int32(0)
	defer func() {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		defer cancel()
		if _, err := q.PlatformRecordMarketingFlowExecution(finishCtx, store.PlatformRecordMarketingFlowExecutionParams{TenantID: p.TenantID, FlowID: p.FlowID, Email: p.Email, LeaseOwner: w.id, LeaseFence: p.LeaseFence, NodeKey: p.StepKey, Status: status, ErrorCode: code, RetrySeconds: retry}); err != nil {
			w.opts.Logger.Warn("marketing flow execution record failed")
		}
		if _, err := q.PlatformReleaseMarketingFlowLease(finishCtx, store.PlatformReleaseMarketingFlowLeaseParams{TenantID: p.TenantID, FlowID: p.FlowID, Email: p.Email, LeaseOwner: pgtype.Text{String: w.id, Valid: true}, LeaseFence: p.LeaseFence}); err != nil {
			w.opts.Logger.Warn("marketing flow claim release failed")
		}
	}()
	steps, err := w.stepMap(ctx, q, p.TenantID, p.FlowID)
	if err != nil {
		status, code, retry = "failed", "configuration_unavailable", 60
		return
	}
	allowed, gateErr := w.store.MayAdvance(ctx, p.TenantID, p.FlowID, p.Email)
	if gateErr != nil || !allowed {
		status, code, retry = "blocked", "not_allowed", 60
		return
	}
	switch p.StepKind {
	case "send":
		err = marketingflow.Advance(ctx, w.store, w.opts.Sender, p.TenantID, p.FlowID, p.Email, "send", p.StepNext)
		var gate *flowGateError
		switch {
		case lease.confirmed || err == nil:
			status = "submitted"
			flowAttempts.WithLabelValues("sent").Inc()
		case errors.Is(err, marketingflow.ErrUncertain):
			status, code = "uncertain", "delivery_uncertain"
			flowAttempts.WithLabelValues("uncertain").Inc()
		case errors.As(err, &gate):
			status, code, retry = "blocked", gate.code, 60
			flowAttempts.WithLabelValues("blocked").Inc()
		case errors.Is(err, marketingflow.ErrNotAllowed):
			status, code, retry = "blocked", "not_allowed", 60
			flowAttempts.WithLabelValues("blocked").Inc()
		default:
			status, code, retry = "failed", "send_failed", 60
			flowAttempts.WithLabelValues("error").Inc()
		}
		if err == nil {
			w.armWait(ctx, q, p, steps)
		}
	case "wait":
		// The entry node may itself be a wait; arm it before trying to advance.
		if _, err = q.PlatformArmMarketingFlowWait(ctx, store.PlatformArmMarketingFlowWaitParams{TenantID: p.TenantID, FlowID: p.FlowID, Email: p.Email, LeaseOwner: pgtype.Text{String: w.id, Valid: true}, LeaseFence: p.LeaseFence}); err != nil {
			status, code, retry = "failed", "wait_failed", 60
			return
		}
		err = w.store.Save(ctx, p.TenantID, p.FlowID, p.Email, marketingflow.Position{Step: p.StepNext})
		switch {
		case err == nil:
			status = "advanced"
			w.armWait(ctx, q, p, steps)
		case errors.Is(err, marketingflow.ErrNotAllowed):
			status = "waiting"
		default:
			status, code, retry = "failed", "wait_failed", 60
		}
	case "condition":
		allowed, checkErr := w.store.MayAdvance(ctx, p.TenantID, p.FlowID, p.Email)
		if checkErr != nil || !allowed {
			status, code, retry = "blocked", "not_allowed", 60
			return
		}
		facts := map[string]string{}
		if json.Unmarshal(p.EventFacts, &facts) != nil {
			status, code, retry = "failed", "invalid_event", 3600
			return
		}
		next := p.StepNext
		if !graph.Condition(graph.Node{Field: p.ConditionField, Operator: p.ConditionOperator, Value: p.ConditionValue}, facts) {
			next = p.StepOtherwise
		}
		err = w.store.Save(ctx, p.TenantID, p.FlowID, p.Email, marketingflow.Position{Step: next})
		if err != nil {
			status, code, retry = "blocked", "not_allowed", 60
			return
		}
		status = "advanced"
		p.StepNext = next
		w.armWait(ctx, q, p, steps)
	default:
		status = "blocked"
	}
}

// armWait records wait_until when the position just landed on a wait step,
// so the next claim only fires once the recorded wait has elapsed.
func (w *FlowWorker) armWait(ctx context.Context, q *store.Queries, p store.PlatformClaimMarketingFlowPositionsRow, steps map[string]flowStep) {
	step, ok := steps[p.StepNext]
	if !ok || step.kind != "wait" || step.waitSeconds <= 0 {
		return
	}
	if _, err := q.PlatformArmMarketingFlowWait(ctx, store.PlatformArmMarketingFlowWaitParams{
		TenantID: p.TenantID, FlowID: p.FlowID, Email: p.Email, LeaseOwner: pgtype.Text{String: w.id, Valid: true}, LeaseFence: p.LeaseFence,
	}); err != nil {
		w.opts.Logger.Warn("marketing flow wait arm failed", zap.Error(err))
	}
}

type flowStep struct {
	kind        string
	waitSeconds int32
}

func (w *FlowWorker) stepMap(ctx context.Context, q *store.Queries, tenant, flow int64) (map[string]flowStep, error) {
	rows, err := q.PlatformListMarketingFlowSteps(ctx, store.PlatformListMarketingFlowStepsParams{TenantID: tenant, FlowID: flow})
	if err != nil {
		return nil, err
	}
	steps := make(map[string]flowStep, len(rows))
	for _, row := range rows {
		steps[row.StepKey] = flowStep{kind: row.Kind, waitSeconds: row.WaitSeconds}
	}
	return steps, nil
}

// clampInt32 confines an operator-provided int to the SQL int4 domain so a
// misconfigured batch/lease cannot wrap into a negative claim parameter.
func clampInt32(v int64) int32 {
	const maxI32 = int64(math.MaxInt32)
	const minI32 = int64(math.MinInt32)
	if v > maxI32 {
		return int32(maxI32)
	}
	if v < minI32 {
		return int32(minI32)
	}
	return int32(v)
}
