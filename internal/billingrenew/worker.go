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
// channel enrolled. Checkout support does not imply recurring capability.
var ErrNoChargingChannel = errors.New("no charging-capable payment channel is enrolled")

// ChargeRequest is one bounded charge of a saved instrument for one renewal
// period. Each definitely declined charge has a distinct generation key.
// Unknown outcomes retain their original key and permit Query only.
type ChargeRequest struct {
	TenantID               int64
	SubscriptionID         int64
	AttemptID              int64
	PeriodEnd              time.Time
	AmountCents            int64
	Currency               string
	IdempotencyKey         string
	MerchantScope          string
	AuthorizationReference string
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

// ChargeCoordinator persists a fenced intent under the billing tenant lock.
type ChargeCoordinator interface {
	BeginCharge(context.Context, store.SubscriptionRenewalAttempts, string) (store.SubscriptionRenewalAttempts, error)
}

type claimStore interface {
	PlatformRecoverRenewalLeases(context.Context) error
	PlatformCompleteRenewalClaim(context.Context, store.PlatformCompleteRenewalClaimParams) (int64, error)
}

// Worker processes bounded charges and read-only reconciliation separately.
type Worker struct {
	Coordinator ChargeCoordinator
	Store       Store
	Chargers    ChargerResolver
	Settlements SettlementStore
}

// RunOnce enqueues due subscriptions and processes one claimed attempt.
func (w Worker) RunOnce(ctx context.Context) error {
	durable, ok := w.Store.(claimStore)
	if !ok {
		return errors.New("renewal durable claim store is required")
	}
	if d, ok := w.Chargers.(*Dispatcher); ok {
		if err := d.RefreshRuntime(ctx); err != nil {
			return err
		}
	}
	if err := durable.PlatformRecoverRenewalLeases(ctx); err != nil {
		return err
	}
	if err := w.Store.PlatformEnqueueDueRenewals(ctx); err != nil {
		return err
	}
	row, err := w.Store.PlatformClaimRenewalAttempt(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	finish := func(state, reason string) error {
		_, e := durable.PlatformCompleteRenewalClaim(ctx, store.PlatformCompleteRenewalClaimParams{ID: row.ID, TenantID: row.TenantID, ClaimToken: row.ClaimToken, Column4: state, LastError: reason})
		return e
	}
	if w.Chargers == nil {
		if row.SideEffectIntentAt.Valid {
			return finish("uncertain", "ADAPTER_UNAVAILABLE")
		}
		return finish("canceled", "ADAPTER_UNAVAILABLE")
	}
	charger, req, err := w.Chargers.ChargerFor(ctx, row)
	if err != nil || charger == nil {
		if row.SideEffectIntentAt.Valid {
			return finish("uncertain", "AUTHORIZATION_RECONCILIATION_REQUIRED")
		}
		return finish("canceled", "AUTHORIZATION_REQUIRED")
	}
	callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var outcome ChargeOutcome
	if row.SideEffectIntentAt.Valid {
		query, ok := charger.(interface {
			Query(context.Context, ChargeRequest) (ChargeOutcome, error)
		})
		if !ok {
			return finish("uncertain", "RECONCILIATION_UNAVAILABLE")
		}
		outcome, err = query.Query(callCtx, req)
	} else {
		if w.Coordinator == nil {
			return finish("canceled", "DURABLE_INTENT_UNAVAILABLE")
		}
		if req.IdempotencyKey == "" {
			req.IdempotencyKey = RenewalDedupeKey(row.SubscriptionID, row.PeriodEnd.Time, row.ID) + fmt.Sprintf(":charge-%d", row.ChargeCount+1)
		}
		if _, err = w.Coordinator.BeginCharge(ctx, row, req.IdempotencyKey); errors.Is(err, pgx.ErrNoRows) {
			return finish("canceled", "ENROLLMENT_CHANGED")
		} else if err != nil {
			return err
		}
		outcome, err = charger.Charge(callCtx, req)
	}
	// Every transport error is ambiguous. Never call Charge for this intent
	// again, even after the provider's idempotency retention window expires.
	if err != nil || outcome.Status == ChargeUncertain || outcome.Status == "" {
		return finish("uncertain", "PROVIDER_UNCERTAIN")
	}
	if outcome.Status == ChargeSucceeded {
		if w.Settlements == nil {
			return finish("uncertain", "SETTLEMENT_UNAVAILABLE")
		}
		if outcome.ProviderTxnID == "" || outcome.AmountCents != req.AmountCents || outcome.Currency != req.Currency {
			return finish("uncertain", "PROVIDER_CHARGE_MISMATCH")
		}
		_, err = w.Settlements.PlatformSettleRenewalAttempt(ctx, RenewalSettlementParams{TenantID: row.TenantID, AttemptID: row.ID, Provider: charger.Name(), AccountScope: req.MerchantScope, ProviderTxnID: outcome.ProviderTxnID, AmountCents: outcome.AmountCents, Currency: outcome.Currency, DedupeKey: req.IdempotencyKey})
		return err
	}
	if outcome.Status != ChargeFailed {
		return finish("uncertain", "PROVIDER_UNCERTAIN")
	}
	if err = finish("failed", "PROVIDER_CHARGE_DECLINED"); err != nil {
		return err
	}
	return w.Store.PlatformEnqueueRenewalFailure(ctx, row.ID)
}
