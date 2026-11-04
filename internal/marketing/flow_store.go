package marketing

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/marketingflow"
	"github.com/bushixiucai/urmail/internal/store"
)

// FlowStore implements marketingflow.Store over the durable flow tables and
// their SECURITY DEFINER commit/check functions. It is the production Store
// the flow tick worker drives Advance with.
type FlowStore struct {
	Pool *db.Pool
}

var _ marketingflow.Store = (*FlowStore)(nil)

func (s *FlowStore) q(_ context.Context) (*store.Queries, error) {
	if s == nil || s.Pool == nil {
		return nil, ErrChannelUnavailable
	}
	return store.New(s.Pool), nil
}

// MayAdvance reports whether the position may send in this window.
// MayAdvance reports whether the position may send in this window.
func (s *FlowStore) MayAdvance(ctx context.Context, tenant, flow int64, email string) (bool, error) {
	q, err := s.q(ctx)
	if err != nil {
		return false, err
	}
	return q.PlatformVersionedMarketingFlowMayAdvance(ctx, store.PlatformVersionedMarketingFlowMayAdvanceParams{TenantID: tenant, RuntimeFlowID: flow, Email: email})
}

// Position loads the stored flow position for one recipient.
// Position loads the stored flow position for one recipient.
func (s *FlowStore) Position(ctx context.Context, tenant, flow int64, email string) (marketingflow.Position, error) {
	q, err := s.q(ctx)
	if err != nil {
		return marketingflow.Position{}, err
	}
	row, err := q.PlatformGetMarketingFlowPosition(ctx, store.PlatformGetMarketingFlowPositionParams{TenantID: tenant, FlowID: flow, Email: email})
	if errors.Is(err, pgx.ErrNoRows) {
		return marketingflow.Position{}, marketingflow.ErrNotAllowed
	}
	if err != nil {
		return marketingflow.Position{}, err
	}
	return marketingflow.Position{Step: row.StepKey, Unsubscribed: row.Unsubscribed}, nil
}

// Save commits the engine's position change through
// urmail_commit_marketing_flow_step, which re-checks unsubscribe, wait,
// uncertainty and the sending gate under FOR UPDATE.
func (s *FlowStore) Save(ctx context.Context, tenant, flow int64, email string, p marketingflow.Position) error {
	q, err := s.q(ctx)
	if err != nil {
		return err
	}
	event := ""
	if p.Unsubscribed {
		// Advance records a terminal unsubscribe through the same commit path.
		event = "unsubscribed"
	}
	var result string
	if lease := leaseFromContext(ctx); lease != nil {
		result, err = q.PlatformCommitMarketingFlowFenced(ctx, store.PlatformCommitMarketingFlowFencedParams{PTenant: tenant, PFlow: flow, PEmail: email, POwner: lease.owner, PFence: lease.fence, PNext: p.Step, PUnsubscribed: p.Unsubscribed, PConfirmed: lease.confirmed})
	} else {
		versioned, checkErr := q.PlatformMarketingFlowIsVersioned(ctx, store.PlatformMarketingFlowIsVersionedParams{TenantID: tenant, RuntimeFlowID: flow})
		if checkErr != nil {
			return checkErr
		}
		if versioned {
			return marketingflow.ErrNotAllowed
		}
		result, err = q.PlatformCommitMarketingFlowWait(ctx, store.PlatformCommitMarketingFlowWaitParams{PTenant: tenant, PFlow: flow, PEmail: email, Event: event, NextKey: p.Step})
	}
	if err != nil {
		return err
	}
	switch result {
	case "uncertain":
		return marketingflow.ErrUncertain
	case "missing", "blocked", "stale_lease":
		return marketingflow.ErrNotAllowed
	}

	return nil
}

// MarkUncertain records an uncertain send for later reconciliation.
// MarkUncertain records an uncertain send for later reconciliation.
func (s *FlowStore) MarkUncertain(ctx context.Context, tenant, flow int64, email string) error {
	q, err := s.q(ctx)
	if err != nil {
		return err
	}
	if lease := leaseFromContext(ctx); lease != nil {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		defer cancel()
		changed, markErr := q.PlatformMarkMarketingFlowUncertainFenced(finishCtx, store.PlatformMarkMarketingFlowUncertainFencedParams{TenantID: tenant, FlowID: flow, Email: email, LeaseOwner: pgtype.Text{String: lease.owner, Valid: true}, LeaseFence: lease.fence})
		if markErr != nil {
			return markErr
		}
		if changed == 0 {
			return marketingflow.ErrNotAllowed
		}
		return nil
	}
	_, err = q.PlatformMarkMarketingFlowUncertain(ctx, store.PlatformMarkMarketingFlowUncertainParams{PTenant: tenant, PFlow: flow, PEmail: email})
	return err
}

// StillAllowed re-checks entitlement before the actual side effect.
// StillAllowed re-checks entitlement before the actual side effect.
func (s *FlowStore) StillAllowed(ctx context.Context, tenant, flow int64, email string) (bool, error) {
	q, err := s.q(ctx)
	if err != nil {
		return false, err
	}
	return q.PlatformMarketingFlowStillAllowed(ctx, store.PlatformMarketingFlowStillAllowedParams{PTenant: tenant, PFlow: flow, PEmail: email})
}

// TemplateFlowContent resolves step content from the tenant template named by
// the step key, the wiring the flow worker supplies in production.
func TemplateFlowContent(pool *db.Pool) FlowContent {
	return func(ctx context.Context, tenantID, flowID int64, step string) (string, string, bool, error) {
		if pool == nil {
			return "", "", false, ErrChannelUnavailable
		}
		q := store.New(pool)
		versioned, err := q.PlatformMarketingFlowIsVersioned(ctx, store.PlatformMarketingFlowIsVersionedParams{TenantID: tenantID, RuntimeFlowID: flowID})
		if err != nil {
			return "", "", false, err
		}
		if versioned {
			content, contentErr := q.PlatformGetMarketingFlowSnapshotContent(ctx, store.PlatformGetMarketingFlowSnapshotContentParams{TenantID: tenantID, RuntimeFlowID: flowID, StepKey: step})
			if contentErr != nil {
				return "", "", false, contentErr
			}
			return content.Subject, content.TextBody, true, nil
		}
		row, err := store.New(pool).PlatformGetMarketingFlowTemplate(ctx, store.PlatformGetMarketingFlowTemplateParams{TenantID: tenantID, Name: step})
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", false, nil
		}
		if err != nil {
			return "", "", false, err
		}
		return row.Subject, row.TextBody, true, nil
	}
}
