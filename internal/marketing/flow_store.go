package marketing

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

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
	return store.New(s.Pool.Pool), nil
}

// MayAdvance reports whether the position may send in this window.
// MayAdvance reports whether the position may send in this window.
func (s *FlowStore) MayAdvance(ctx context.Context, tenant, flow int64, email string) (bool, error) {
	q, err := s.q(ctx)
	if err != nil {
		return false, err
	}
	return q.PlatformMarketingFlowMayAdvance(ctx, store.PlatformMarketingFlowMayAdvanceParams{PTenant: tenant, PFlow: flow, PEmail: email})
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
	result, err := q.PlatformCommitMarketingFlowWait(ctx, store.PlatformCommitMarketingFlowWaitParams{PTenant: tenant, PFlow: flow, PEmail: email, Event: event, NextKey: p.Step})
	if err != nil {
		return err
	}
	if result == "missing" {
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
	return func(ctx context.Context, tenantID int64, step string) (string, string, bool, error) {
		if pool == nil {
			return "", "", false, ErrChannelUnavailable
		}
		row, err := store.New(pool.Pool).PlatformGetMarketingFlowTemplate(ctx, store.PlatformGetMarketingFlowTemplateParams{TenantID: tenantID, Name: step})
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", false, nil
		}
		if err != nil {
			return "", "", false, err
		}
		return row.Subject, row.TextBody, true, nil
	}
}
