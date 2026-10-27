package billingrenew

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/store"
)

// Charge outcome statuses reported by a Charger.
const (
	ChargeSucceeded = "succeeded"
	ChargeFailed    = "failed"
	ChargeUncertain = "uncertain"
)

// ErrNoChargingChannel means the subscription has no charging-capable payment
// channel enrolled. Channels cannot charge saved instruments yet, so this is
// the current reality for every subscription.
var ErrNoChargingChannel = errors.New("no charging-capable payment channel is enrolled")

// ChargeRequest is one bounded charge of a saved instrument for one renewal
// period. IdempotencyKey (subscription_id:period_end:attempt_id) must be
// passed to the provider so a retried or uncertain charge can never bill twice.
type ChargeRequest struct {
	TenantID       int64
	SubscriptionID int64
	AttemptID      int64
	PeriodEnd      time.Time
	AmountCents    int64
	Currency       string
	IdempotencyKey string
}

// ChargeOutcome carries the provider's answer. Uncertain means the provider
// neither confirmed nor declined the charge (timeout, unknown state).
type ChargeOutcome struct {
	Status        string // ChargeSucceeded, ChargeFailed, ChargeUncertain
	ProviderTxnID string
	AmountCents   int64
	Currency      string
}

// Charger is a payment channel that can charge a saved instrument.
type Charger interface {
	Name() string
	Charge(ctx context.Context, req ChargeRequest) (ChargeOutcome, error)
}

// ChargerResolver looks up the charging channel for a claimed renewal attempt.
// It returns ErrNoChargingChannel when none is enrolled.
type ChargerResolver interface {
	ChargerFor(ctx context.Context, attempt store.SubscriptionRenewalAttempts) (Charger, ChargeRequest, error)
}

// Store is the platform-scoped renewal queue.
type Store interface {
	PlatformEnqueueDueRenewals(context.Context) error
	PlatformClaimRenewalAttempt(context.Context) (store.SubscriptionRenewalAttempts, error)
	PlatformFailRenewalAttempt(context.Context, store.PlatformFailRenewalAttemptParams) error
	PlatformEnqueueRenewalFailure(context.Context, int64) error
	PlatformMarkRenewalAttemptUncertain(context.Context, store.PlatformMarkRenewalAttemptUncertainParams) error
	PlatformSetRenewalDedupeKey(context.Context, store.PlatformSetRenewalDedupeKeyParams) error
	PlatformGetRenewalChargeContext(context.Context, int64) (store.PlatformGetRenewalChargeContextRow, error)
}

// Worker records due renewals as explicit payment failures.
// Without a ChargerResolver (or with no charging-capable channel enrolled) it
// has no payment provider and must not mark an order paid. When a confirmed
// charge succeeds, settlement goes through the idempotent
// PlatformSettleRenewalAttempt path via Settlements.
type Worker struct {
	Store       Store
	Chargers    ChargerResolver
	Settlements SettlementStore
}

// RunOnce enqueues due subscriptions and processes one claimed attempt.
func (w Worker) RunOnce(ctx context.Context) error {
	if err := w.Store.PlatformEnqueueDueRenewals(ctx); err != nil {
		return err
	}
	row, err := w.Store.PlatformClaimRenewalAttempt(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if w.Chargers != nil {
		charger, req, resolveErr := w.Chargers.ChargerFor(ctx, row)
		if resolveErr != nil && !errors.Is(resolveErr, ErrNoChargingChannel) {
			return resolveErr
		}
		if resolveErr == nil && charger != nil {
			return w.charge(ctx, row, charger, req)
		}
	}
	// No charging-capable channel: the explicit, retryable failure path.
	return w.fail(ctx, row, "NO_SAVED_PAYMENT_METHOD")
}

// charge applies one provider answer. It never charges blindly again: an
// uncertain outcome defers the attempt and relies on the provider idempotency
// key when the retry eventually runs.
func (w Worker) charge(ctx context.Context, row store.SubscriptionRenewalAttempts, charger Charger, req ChargeRequest) error {
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = RenewalDedupeKey(row.SubscriptionID, row.PeriodEnd.Time, row.ID)
	}
	if err := w.Store.PlatformSetRenewalDedupeKey(ctx, store.PlatformSetRenewalDedupeKeyParams{
		ID: row.ID, DedupeKey: req.IdempotencyKey, TenantID: row.TenantID,
	}); err != nil {
		return err
	}
	outcome, chargeErr := charger.Charge(ctx, req)
	status := outcome.Status
	if chargeErr != nil {
		status = ChargeFailed
	}
	switch status {
	case ChargeSucceeded:
		if w.Settlements == nil {
			// A confirmed charge without a settlement path must fail loudly
			// rather than leave money taken without entitlements.
			return errors.New("renewal charge succeeded but settlement store is not configured")
		}
		if outcome.ProviderTxnID == "" || outcome.AmountCents <= 0 {
			return w.fail(ctx, row, "PROVIDER_CHARGE_INCOMPLETE")
		}
		if _, err := w.Settlements.PlatformSettleRenewalAttempt(ctx, RenewalSettlementParams{
			TenantID: row.TenantID, AttemptID: row.ID,
			Provider: charger.Name(), ProviderTxnID: outcome.ProviderTxnID,
			AmountCents: outcome.AmountCents, Currency: outcome.Currency, DedupeKey: req.IdempotencyKey,
		}); err != nil && !errors.Is(err, ErrRenewalAttemptCanceled) {
			return err
		}
		return nil
	case ChargeUncertain:
		return w.Store.PlatformMarkRenewalAttemptUncertain(ctx, store.PlatformMarkRenewalAttemptUncertainParams{
			ID: row.ID, TenantID: row.TenantID,
		})
	default:
		return w.fail(ctx, row, "PROVIDER_CHARGE_DECLINED")
	}
}

// fail records the explicit retryable failure and notifies the tenant once
// per attempt (the notification dedupe key includes the attempt counter).
func (w Worker) fail(ctx context.Context, row store.SubscriptionRenewalAttempts, reason string) error {
	if err := w.Store.PlatformFailRenewalAttempt(ctx, store.PlatformFailRenewalAttemptParams{ID: row.ID, LastError: reason}); err != nil {
		return err
	}
	if err := w.Store.PlatformEnqueueRenewalFailure(ctx, row.ID); err != nil {
		return fmt.Errorf("notify renewal failure: %w", err)
	}
	return nil
}
