// Package billingrenew owns the paid auto-renewal cycle: the due-renewal
// worker records explicit charge attempts, and the idempotent settlement path
// applies confirmed provider money exactly once.
package billingrenew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// RenewalAccountScope separates renewal settlement receipts from order
// checkout receipts inside the payments provider_txn_id unique index.
const RenewalAccountScope = "renewal"

// ErrRenewalAttemptCanceled means a canceled attempt can never be settled.
var ErrRenewalAttemptCanceled = errors.New("renewal attempt canceled")

// ErrRenewalSettlementConflict means the provider transaction belongs to
// another tenant or payment, so it must never settle this attempt.
var ErrRenewalSettlementConflict = errors.New("renewal settlement conflict")

// RenewalSettlementParams identifies one idempotent settlement.
type RenewalSettlementParams struct {
	TenantID      int64
	AttemptID     int64
	Provider      string
	AccountScope  string
	ProviderTxnID string
	AmountCents   int64
	Currency      string
	// DedupeKey is the provider idempotency key
	// (subscription_id:period_end:attempt_id) when one was issued.
	DedupeKey string
}

// RenewalSettlement reports what the idempotent settlement did.
type RenewalSettlement struct {
	AlreadySettled bool
	AttemptID      int64
	PaymentID      int64
	PeriodEnd      time.Time
	Status         string
}

// PlatformSettleRenewalAttempt applies a confirmed provider settlement exactly
// once inside a single transaction: it row-locks the attempt, records the
// payment guarded by the payments provider_txn_id unique index, extends the
// subscription period monotonically (never shortening it), restores billing
// status only from active/past_due, and marks the attempt succeeded.
//
// It only changes billing state: independent security suspensions, revoked
// grants and legal holds are not touched. Refunds and chargebacks keep their
// existing separate flows.
//
// Idempotency:
//   - an already-succeeded attempt returns the existing settlement without a
//     second payment or a second extension;
//   - a duplicate provider transaction id (unique index violation) is treated
//     as already settled for this tenant and never extends the period twice;
//   - concurrent callers serialize on the attempt row lock.
func PlatformSettleRenewalAttempt(ctx context.Context, pool *db.Pool, params RenewalSettlementParams) (RenewalSettlement, error) {
	var result RenewalSettlement
	if pool == nil {
		return result, errors.New("settlement requires a platform pool")
	}
	params.Provider = strings.TrimSpace(params.Provider)
	params.ProviderTxnID = strings.TrimSpace(params.ProviderTxnID)
	params.Currency = strings.ToUpper(strings.TrimSpace(params.Currency))
	if params.Provider == "" || params.ProviderTxnID == "" {
		return result, errors.New("settlement requires provider and transaction id")
	}
	if params.AmountCents <= 0 || (params.Currency != "CNY" && params.Currency != "USD") {
		return result, fmt.Errorf("settlement amount %d %s is not a positive supported total", params.AmountCents, params.Currency)
	}
	if params.TenantID < 1 || params.AttemptID < 1 {
		return result, errors.New("settlement requires tenant and attempt ids")
	}
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(ctx, params.TenantID); err != nil {
		return result, err
	}

	attempt, err := q.PlatformLockRenewalAttempt(ctx, store.PlatformLockRenewalAttemptParams{ID: params.AttemptID, TenantID: params.TenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return result, fmt.Errorf("renewal attempt %d: %w", params.AttemptID, pgx.ErrNoRows)
		}
		return result, err
	}
	if attempt.AmountCents.Valid && (attempt.AmountCents.Int64 != params.AmountCents || attempt.Currency.String != params.Currency || attempt.MerchantScope != params.AccountScope || (attempt.DedupeKey != "" && attempt.DedupeKey != params.DedupeKey)) {
		return result, ErrRenewalSettlementConflict
	}
	scope := attempt.MerchantScope
	if scope == "" {
		scope = RenewalAccountScope
	}
	if attempt.MerchantScope != "" && !strings.HasPrefix(scope, params.Provider+":") {
		return result, ErrRenewalSettlementConflict
	}
	result.AttemptID = attempt.ID
	if attempt.State == "succeeded" {
		existing, lookupErr := q.PlatformGetRenewalPayment(ctx, store.PlatformGetRenewalPaymentParams{Provider: params.Provider, AccountScope: scope, ProviderTxnID: pgtype.Text{String: params.ProviderTxnID, Valid: true}})
		if lookupErr != nil {
			return result, ErrRenewalSettlementConflict
		}
		var receipt struct {
			AttemptID int64 `json:"renewal_attempt_id"`
		}
		if json.Unmarshal(existing.Raw, &receipt) != nil || receipt.AttemptID != attempt.ID || existing.TenantID != params.TenantID || existing.AmountCents != params.AmountCents || existing.Currency != params.Currency {
			return result, ErrRenewalSettlementConflict
		}

		// Duplicate settlement call: return the existing settlement without a
		// second payment and without extending the period again.
		if err = tx.Commit(ctx); err != nil {
			return result, err
		}
		result.AlreadySettled = true
		return result, nil
	}
	if attempt.State == "canceled" {
		return result, ErrRenewalAttemptCanceled
	}

	raw, err := json.Marshal(map[string]any{
		"renewal_attempt_id": attempt.ID,
		"dedupe_key":         params.DedupeKey,
	})
	if err != nil {
		return result, err
	}
	payment, err := q.PlatformInsertRenewalPayment(ctx, store.PlatformInsertRenewalPaymentParams{
		TenantID:      params.TenantID,
		Provider:      params.Provider,
		AccountScope:  scope,
		ProviderTxnID: pgtype.Text{String: params.ProviderTxnID, Valid: true},
		AmountCents:   params.AmountCents,
		Currency:      params.Currency,
		Raw:           raw,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The provider transaction id already exists: idempotent success, but
		// only when the existing receipt belongs to this same tenant. Never
		// extend the period again on a duplicate confirmation.
		existing, lookupErr := q.PlatformGetRenewalPayment(ctx, store.PlatformGetRenewalPaymentParams{
			Provider: params.Provider, AccountScope: scope, ProviderTxnID: pgtype.Text{String: params.ProviderTxnID, Valid: true},
		})
		if lookupErr != nil {
			return result, lookupErr
		}
		var receipt struct {
			AttemptID int64 `json:"renewal_attempt_id"`
		}
		_ = json.Unmarshal(existing.Raw, &receipt)
		if receipt.AttemptID != attempt.ID || existing.TenantID != params.TenantID || existing.AmountCents != params.AmountCents || existing.Currency != params.Currency {
			return result, ErrRenewalSettlementConflict
		}
		if params.DedupeKey != "" && attempt.DedupeKey == "" {
			if err = q.PlatformSetRenewalDedupeKey(ctx, store.PlatformSetRenewalDedupeKeyParams{ID: attempt.ID, DedupeKey: params.DedupeKey, TenantID: params.TenantID}); err != nil {
				return result, err
			}
		}
		if err = q.PlatformMarkRenewalAttemptSucceeded(ctx, store.PlatformMarkRenewalAttemptSucceededParams{ID: attempt.ID, TenantID: params.TenantID}); err != nil {
			return result, err
		}
		if err = tx.Commit(ctx); err != nil {
			return result, err
		}
		result.AlreadySettled = true
		result.PaymentID = existing.ID
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("record renewal payment: %w", err)
	}
	result.PaymentID = payment.ID

	extended, err := q.PlatformExtendSubscriptionForRenewal(ctx, store.PlatformExtendSubscriptionForRenewalParams{
		ID: attempt.SubscriptionID, TenantID: params.TenantID, ExpectedPeriodEnd: attempt.PeriodEnd, RenewalPeriodEnd: attempt.RenewalPeriodEnd,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, fmt.Errorf("extend subscription for renewal: %w", err)
	}
	if err == nil {
		result.PeriodEnd = extended.PeriodEnd.Time
		result.Status = extended.Status
		if attempt.EnrollmentID.Valid {
			next, periodErr := renewalEnd(extended.PeriodEnd.Time, attempt.BillingCycle.String, attempt.CalendarSnapshot)
			if periodErr != nil {
				return result, periodErr
			}
			if err = q.PlatformAdvanceRenewalEnrollment(ctx, store.PlatformAdvanceRenewalEnrollmentParams{ID: attempt.EnrollmentID.Int64, TenantID: params.TenantID, ExpectedPeriodEnd: extended.PeriodEnd, RenewalPeriodEnd: pgtype.Timestamptz{Time: next, Valid: true}}); err != nil {
				return result, err
			}
		}
	}
	if attempt.AmountCents.Valid {
		var frozen struct {
			Code string `json:"code"`
		}
		if e := json.Unmarshal(attempt.PlanSnapshot, &frozen); e != nil || frozen.Code == "" {
			return result, errors.New("renewal plan snapshot is invalid")
		}
		order, e := q.PlatformCreateRenewalOrder(ctx, store.PlatformCreateRenewalOrderParams{TenantID: params.TenantID, OrderNo: fmt.Sprintf("RN-%d", attempt.ID), AmountCents: params.AmountCents, Currency: params.Currency, PlanCode: pgtype.Text{String: frozen.Code, Valid: true}, IdempotencyKey: pgtype.Text{String: "renewal:" + fmt.Sprint(attempt.ID), Valid: true}, BillingCycle: attempt.BillingCycle.String})
		if e != nil {
			return result, e
		}
		if e = q.PlatformBindRenewalPaymentOrder(ctx, store.PlatformBindRenewalPaymentOrderParams{ID: payment.ID, TenantID: params.TenantID, OrderID: pgtype.Int8{Int64: order.ID, Valid: true}}); e != nil {
			return result, e
		}
		if errors.Is(err, pgx.ErrNoRows) {
			if e = q.PlatformRecordPaymentManualReview(ctx, store.PlatformRecordPaymentManualReviewParams{TenantID: params.TenantID, OrderID: order.ID, PaymentID: payment.ID, Reason: "RENEWAL_CYCLE_ALREADY_FUNDED"}); e != nil {
				return result, e
			}
		} else {
			if e = q.PlatformRecordSubscriptionTerm(ctx, store.PlatformRecordSubscriptionTermParams{OrderID: order.ID, TenantID: params.TenantID, PeriodStart: attempt.PeriodEnd, PeriodEnd: extended.PeriodEnd, BillingPeriodStart: attempt.PeriodEnd, PlanSnapshot: attempt.PlanSnapshot, BaseAmountCents: params.AmountCents, Currency: params.Currency, BillingCycle: attempt.BillingCycle.String, CalendarSnapshot: attempt.CalendarSnapshot}); e != nil {
				return result, e
			}
			invoice, e := q.PlatformCreateSettlementInvoice(ctx, store.PlatformCreateSettlementInvoiceParams{TenantID: params.TenantID, OrderID: pgtype.Int8{Int64: order.ID, Valid: true}, Number: fmt.Sprintf("RN-%d", attempt.ID), PeriodStart: attempt.PeriodEnd, PeriodEnd: extended.PeriodEnd, AmountCents: params.AmountCents, Currency: params.Currency})
			if e != nil {
				return result, e
			}
			document, e := CaptureInvoiceDocumentSnapshot(ctx, q, params.TenantID, params.AmountCents, params.Currency, "observed_at_settlement")
			if e != nil {
				return result, e
			}
			if _, e = q.PlatformFreezeInvoiceDocumentSnapshot(ctx, store.PlatformFreezeInvoiceDocumentSnapshotParams{TenantID: params.TenantID, ID: invoice.ID, DocumentSnapshot: document}); e != nil {
				return result, e
			}
			if e = q.PlatformBindInvoicePayment(ctx, store.PlatformBindInvoicePaymentParams{TenantID: params.TenantID, OrderID: pgtype.Int8{Int64: order.ID, Valid: true}, SourcePaymentID: pgtype.Int8{Int64: payment.ID, Valid: true}}); e != nil {
				return result, e
			}
		}
	}
	// A missing or non-restorable subscription (for example canceled) keeps
	// the confirmed money recorded but does not resurrect entitlements.
	if err = q.PlatformMarkRenewalAttemptSucceeded(ctx, store.PlatformMarkRenewalAttemptSucceededParams{ID: attempt.ID, TenantID: params.TenantID}); err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}

// ConfirmedRenewalEvent is a verified provider confirmation that may settle a
// renewal attempt in addition to, or instead of, an order.
type ConfirmedRenewalEvent struct {
	Provider      string
	AccountScope  string
	ProviderTxnID string
	AmountCents   int64
	Currency      string
	// Raw is the verified event payload. It is scanned only for the
	// idempotency key this system issued to the provider.
	Raw []byte
}

// RouteConfirmedWebhook routes a verified webhook confirmation through the
// idempotent settlement path. Duplicate and out-of-order deliveries are
// no-ops: a payment already attached to a renewal attempt is never settled
// again, and an attempt that is not pending/failed cannot be settled.
// It reports whether the event belonged to a renewal attempt.
func RouteConfirmedWebhook(ctx context.Context, pool *db.Pool, event ConfirmedRenewalEvent) (bool, error) {
	if pool == nil || event.Provider == "" || event.ProviderTxnID == "" {
		return false, nil
	}
	q := store.New(pool)
	scopes := []string{RenewalAccountScope, event.AccountScope}
	for _, scope := range scopes {
		if scope == "" {
			continue
		}
		existingAttempt, err := q.PlatformFindRenewalAttemptForPayment(ctx, store.PlatformFindRenewalAttemptForPaymentParams{
			Provider: event.Provider, AccountScope: scope, ProviderTxnID: pgtype.Text{String: event.ProviderTxnID, Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		// A duplicate must retain its original account, amount and currency.
		_, err = PlatformSettleRenewalAttempt(ctx, pool, RenewalSettlementParams{TenantID: existingAttempt.TenantID, AttemptID: existingAttempt.ID, Provider: event.Provider, AccountScope: event.AccountScope, ProviderTxnID: event.ProviderTxnID, AmountCents: event.AmountCents, Currency: event.Currency, DedupeKey: existingAttempt.DedupeKey})
		return err == nil, err
	}
	key := RenewalDedupeKeyFromPayload(event.Raw)
	if key == "" {
		return false, nil
	}
	attempt, err := q.PlatformFindRenewalAttemptByDedupeKey(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	currency := strings.ToUpper(strings.TrimSpace(event.Currency))
	_, err = PlatformSettleRenewalAttempt(ctx, pool, RenewalSettlementParams{
		TenantID: attempt.TenantID, AttemptID: attempt.ID,
		Provider: event.Provider, AccountScope: event.AccountScope, ProviderTxnID: event.ProviderTxnID,
		AmountCents: event.AmountCents, Currency: currency, DedupeKey: key,
	})
	if errors.Is(err, ErrRenewalAttemptCanceled) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// RenewalDedupeKeyFromPayload extracts the idempotency key this system issued
// from a verified provider payload. Providers echo it at the top level or
// inside their metadata object. It never trusts a key embedded in a nested
// data envelope of an unverified source: callers pass verified payloads only.
func RenewalDedupeKeyFromPayload(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	if key, ok := payload["idempotency_key"].(string); ok {
		return strings.TrimSpace(key)
	}
	visit := func(candidate any) string {
		if object, ok := candidate.(map[string]any); ok {
			if key, ok := object["idempotency_key"].(string); ok {
				return strings.TrimSpace(key)
			}
		}
		return ""
	}
	if key := visit(payload["metadata"]); key != "" {
		return key
	}
	if data, ok := payload["data"].(map[string]any); ok {
		if key := visit(data["object"]); key != "" {
			return key
		}
		if object, ok := data["object"].(map[string]any); ok {
			if key := visit(object["metadata"]); key != "" {
				return key
			}
		}
		if key := visit(data["metadata"]); key != "" {
			return key
		}
	}
	return ""
}

// RenewalDedupeKey builds the provider idempotency key for one attempt.
func RenewalDedupeKey(subscriptionID int64, periodEnd time.Time, attemptID int64) string {
	return fmt.Sprintf("%d:%s:%d", subscriptionID, periodEnd.UTC().Format("20060102T150405Z"), attemptID)
}

// SettlementStore lets the worker apply a charged outcome through the
// idempotent settlement path.
type SettlementStore interface {
	PlatformSettleRenewalAttempt(ctx context.Context, params RenewalSettlementParams) (RenewalSettlement, error)
}

// PoolSettlementStore adapts a platform pool to SettlementStore.
type PoolSettlementStore struct{ Pool *db.Pool }

// PlatformSettleRenewalAttempt implements SettlementStore.
func (s PoolSettlementStore) PlatformSettleRenewalAttempt(ctx context.Context, params RenewalSettlementParams) (RenewalSettlement, error) {
	return PlatformSettleRenewalAttempt(ctx, s.Pool, params)
}
