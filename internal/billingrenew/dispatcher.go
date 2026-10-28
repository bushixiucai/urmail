package billingrenew

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

// RecurringAdapter is deliberately separate from checkout support. Registering
// a checkout provider never establishes saved-instrument charging capability.
// Query must not create or retry any financial operation, including not-found.
type RecurringAdapter interface {
	Charger
	Query(context.Context, ChargeRequest) (ChargeOutcome, error)
	VerifyAuthorization(context.Context, payment.Merchant, string) (VerifiedAuthorization, error)
	VerifyMerchant(context.Context, payment.Merchant) (MerchantEvidence, error)
}

// VerifiedAuthorization is returned by a provider lookup, never tenant input.
type VerifiedAuthorization struct {
	ActorID                    int64
	RequestID                  string
	TenantID, SubscriptionID   int64
	Reference, Label, Currency string
	ExpiresAt                  time.Time
}

// MerchantEvidence records an adapter's bounded merchant capability check.
type MerchantEvidence struct {
	Reference string
	ExpiresAt time.Time
}

// SecretBox seals authorization references at rest.
type SecretBox interface {
	EncryptString(string) (string, error)
	DecryptString(string) (string, error)
}

// Dispatcher resolves only approved, configuration-bound, unexpired evidence.
// Concrete production adapters still require current merchant approval and
// evidence before use. Sandbox-only adapters belong to isolated tests.
type Dispatcher struct {
	Pool     *db.Pool
	Registry *payment.Registry
	Secrets  SecretBox
	Adapters map[string]RecurringAdapter
}

func (d *Dispatcher) capabilities(ctx context.Context) ([]store.PlatformListRecurringCapabilitiesRow, error) {
	if d == nil || d.Pool == nil {
		return nil, ErrNoChargingChannel
	}
	return store.New(d.Pool).PlatformListRecurringCapabilities(ctx)
}

func (d *Dispatcher) adapter(c store.PlatformListRecurringCapabilitiesRow, reconcile bool) RecurringAdapter {
	if d == nil {
		return nil
	}
	a := d.Adapters[c.AdapterKey]
	if a == nil || a.Name() != c.Provider {
		return nil
	}
	// Reconciliation is read-only and must remain possible after revocation,
	// expiry or merchant disablement. The frozen account is checked separately.
	if reconcile {
		return a
	}
	if !c.Enabled || !c.MerchantApproved || !c.EvidenceAt.Valid || !c.EvidenceExpiresAt.Valid || !c.EvidenceExpiresAt.Time.After(time.Now()) || !c.EvidenceConfigAt.Valid || !c.EvidenceConfigAt.Time.Equal(c.MerchantConfigAt.Time) {
		return nil
	}
	return a
}

// RefreshRuntime refreshes existing technical evidence and closes unavailable runtime.
func (d *Dispatcher) RefreshRuntime(ctx context.Context) error {
	if d == nil || d.Pool == nil {
		return ErrNoChargingChannel
	}
	refreshErr := d.refreshMerchantEvidence(ctx)
	// Never use the pre-network snapshot to restore readiness after revocation.
	rows, err := d.capabilities(ctx)
	if err != nil {
		return err
	}
	q := store.New(d.Pool)
	for _, c := range rows {
		if err = q.PlatformConvergeRecurringRuntime(ctx, store.PlatformConvergeRecurringRuntimeParams{MerchantID: c.MerchantID, AdapterAvailable: d.adapter(c, true) != nil, AdapterKey: c.AdapterKey, Provider: c.Provider}); err != nil {
			return err
		}
	}
	if err = q.PlatformInvalidateRenewalEnrollments(ctx); err != nil {
		return err
	}
	if err = q.PlatformCancelIneligibleRenewals(ctx); err != nil {
		return err
	}
	return refreshErr
}

// ChargerFor never reconstructs a price from the mutable plan catalog.
func (d *Dispatcher) ChargerFor(ctx context.Context, row store.SubscriptionRenewalAttempts) (Charger, ChargeRequest, error) {
	req := ChargeRequest{TenantID: row.TenantID, SubscriptionID: row.SubscriptionID, AttemptID: row.ID, PeriodEnd: row.PeriodEnd.Time, AmountCents: row.AmountCents.Int64, Currency: row.Currency.String, IdempotencyKey: row.DedupeKey, MerchantScope: row.MerchantScope}
	if d == nil || d.Registry == nil || d.Secrets == nil || !row.MerchantID.Valid || !row.BindingID.Valid || !row.AmountCents.Valid || req.AmountCents <= 0 {
		return nil, req, ErrNoChargingChannel
	}
	reconcile := row.SideEffectIntentAt.Valid
	rows, err := d.capabilities(ctx)
	if err != nil {
		return nil, req, err
	}
	var adapter RecurringAdapter
	for _, c := range rows {
		if c.MerchantID == row.MerchantID.Int64 {
			adapter = d.adapter(c, reconcile)
		}
	}
	if adapter == nil {
		return nil, req, ErrNoChargingChannel
	}
	merchant, _, err := d.Registry.Load(ctx, row.MerchantID.Int64, !reconcile)
	if err != nil {
		return nil, req, err
	}
	if merchant.Scope() != row.MerchantScope {
		return nil, req, ErrNoChargingChannel
	}
	b, err := store.New(d.Pool).PlatformGetRenewalBinding(ctx, store.PlatformGetRenewalBindingParams{TenantID: row.TenantID, ID: row.BindingID.Int64})
	if err != nil {
		return nil, req, err
	}
	if b.SubscriptionID != row.SubscriptionID || b.MerchantID != row.MerchantID.Int64 || (!reconcile && (b.RevokedAt.Valid || (b.ExpiresAt.Valid && !b.ExpiresAt.Time.After(time.Now())))) {
		return nil, req, ErrNoChargingChannel
	}
	req.AuthorizationReference, err = d.Secrets.DecryptString(b.AuthorizationCiphertext)
	if err != nil {
		return nil, req, errors.New("renewal authorization cannot be decrypted")
	}
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = RenewalDedupeKey(row.SubscriptionID, row.PeriodEnd.Time, row.ID) + fmt.Sprintf(":charge-%d", row.ChargeCount+1)
	}
	return adapter, req, nil
}

// PoolCoordinator serializes charge intent with cancellation and settlement.
type PoolCoordinator struct{ Pool *db.Pool }

// BeginCharge serializes persisted intent with all tenant billing transitions.
func (c PoolCoordinator) BeginCharge(ctx context.Context, row store.SubscriptionRenewalAttempts, key string) (store.SubscriptionRenewalAttempts, error) {
	tx, err := c.Pool.PlatformTx(ctx)
	if err != nil {
		return row, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(ctx, row.TenantID); err != nil {
		return row, err
	}
	row, err = q.PlatformBeginRenewalCharge(ctx, store.PlatformBeginRenewalChargeParams{ID: row.ID, TenantID: row.TenantID, ClaimToken: row.ClaimToken, DedupeKey: key})
	if err != nil {
		return row, err
	}
	err = tx.Commit(ctx)
	return row, err
}
