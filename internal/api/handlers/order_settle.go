package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/domain/distribution"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

// ErrOrderNotFound means an order does not exist or cannot be settled.
var ErrOrderNotFound = errors.New("order not found")

// ErrPaymentVerificationFailed indicates a mismatch between order and payment details.
var ErrPaymentVerificationFailed = errors.New("payment verification failed")

// SettleOptions carries optional provider transaction details for audit and idempotency.
type SettleOptions struct {
	Provider      string
	ProviderTxnID string
	EventID       string
	AmountCents   int64
	Currency      string
	RawPayload    []byte
}

// SettleOrder marks a pending order paid using manual/default settlement options.
func SettleOrder(ctx context.Context, pool *db.Pool, id int64) error {
	return SettleOrderWithDetails(ctx, pool, id, SettleOptions{
		Provider: payment.ProviderManual,
	})
}

// SettleOrderWithDetails marks a pending order paid, verifies amount/currency,
// computes the correct renewal period preserving remaining days, records
// payment and payment_event rows, replaces the tenant subscription, and issues an invoice.
func SettleOrderWithDetails(ctx context.Context, pool *db.Pool, id int64, opts SettleOptions) error {
	// A provider must supply a positive, verifiable total. Zero can never mean
	// "unknown" here, because it would bypass the order amount comparison.
	if opts.Provider != payment.ProviderManual && opts.AmountCents <= 0 {
		return fmt.Errorf("%w: provider %q reported missing or non-positive amount", ErrPaymentVerificationFailed, opts.Provider)
	}
	if pool == nil {
		return errors.New("database pool is not configured")
	}
	tenantID, err := store.New(pool).PlatformGetOrderTenantID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOrderNotFound
		}
		return err
	}
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Deduplicate event at the transaction boundary if EventID is provided.
	// The insert goes through a platform-only sqlc query instead of a
	// hand-written statement; on conflict the RETURNING clause yields no rows,
	// which signals that the event was already processed idempotently.
	if opts.EventID != "" && opts.Provider != "" {
		payload := opts.RawPayload
		if len(payload) == 0 {
			payload = []byte("{}")
		}
		queries := store.New(tx)
		_, err := queries.PlatformInsertPaymentEventPlaceholder(ctx, store.PlatformInsertPaymentEventPlaceholderParams{
			Provider:        opts.Provider,
			ProviderEventID: opts.EventID,
			Payload:         payload,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Do not trust an event ID alone: only suppress a replay when its
			// persisted JSON payload is semantically identical to the original event.
			// A reused provider/event key with altered contents is a verification failure.
			existingEvent, lookupErr := queries.PlatformGetPaymentEventByProviderAndID(ctx, store.PlatformGetPaymentEventByProviderAndIDParams{
				Provider: opts.Provider, ProviderEventID: opts.EventID,
			})
			if lookupErr != nil {
				return fmt.Errorf("lookup duplicate payment event: %w", lookupErr)
			}
			var originalJSON, replayJSON any
			if json.Unmarshal(existingEvent.Payload, &originalJSON) != nil || json.Unmarshal(payload, &replayJSON) != nil || !reflect.DeepEqual(originalJSON, replayJSON) {
				return fmt.Errorf("%w: provider event ID reused with different payload", ErrPaymentVerificationFailed)
			}
			return tx.Commit(ctx)
		}
		if err != nil {
			return fmt.Errorf("record payment event: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", tenantID); err != nil {
		return err
	}

	queries := store.New(tx)
	order, err := queries.PlatformGetOrderForSettlement(ctx, store.PlatformGetOrderForSettlementParams{TenantID: tenantID, ID: id})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOrderNotFound
		}
		return err
	}
	if order.Status == "paid" {
		return tx.Commit(ctx)
	}
	if order.Status != "pending" {
		return ErrOrderNotFound
	}
	if opts.Provider != "" && opts.Provider != payment.ProviderManual && opts.ProviderTxnID != "" {
		existingPayment, lookupErr := queries.PlatformGetPaymentByProviderTransaction(ctx, store.PlatformGetPaymentByProviderTransactionParams{
			Provider: opts.Provider, ProviderTxnID: pgtype.Text{String: opts.ProviderTxnID, Valid: true},
		})
		if lookupErr == nil {
			if existingPayment.TenantID != tenantID || !existingPayment.OrderID.Valid || existingPayment.OrderID.Int64 != id || existingPayment.AmountCents != order.AmountCents || !strings.EqualFold(existingPayment.Currency, order.Currency) {
				return fmt.Errorf("%w: provider transaction is already attached to another order", ErrPaymentVerificationFailed)
			}
		} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return fmt.Errorf("lookup provider transaction: %w", lookupErr)
		}
	}
	amount, channelID := order.AmountCents, order.ChannelID
	planCode, currency, billingCycle := "", order.Currency, order.BillingCycle
	if order.PlanCode.Valid {
		planCode = order.PlanCode.String
	}

	// Provider settlements require an exact, positive amount match. Zero is not
	// a valid sentinel because it would let a malformed event bypass verification.
	if opts.Provider != payment.ProviderManual && opts.AmountCents != amount {
		return fmt.Errorf("%w: expected amount %d, got %d", ErrPaymentVerificationFailed, amount, opts.AmountCents)
	}
	if opts.Provider == payment.ProviderManual && opts.AmountCents > 0 && opts.AmountCents != amount {
		return fmt.Errorf("%w: expected amount %d, got %d", ErrPaymentVerificationFailed, amount, opts.AmountCents)
	}
	if opts.Currency != "" && !strings.EqualFold(opts.Currency, currency) {
		return fmt.Errorf("%w: expected currency %s, got %s", ErrPaymentVerificationFailed, currency, opts.Currency)
	}

	// Compute accurate period using domain pricing rules (preserves remaining
	// active days). An unknown cycle can only come from a data integrity bug
	// (the column is CHECK-constrained), and silently billing a year-long order
	// as one month would undercharge the tenant: fail the settlement instead so
	// the provider retries and the operator sees the problem (M5 review 3.30).
	currentPeriodEnd, periodErr := queries.PlatformGetLatestActiveSubscriptionPeriodEnd(ctx, tenantID)
	if periodErr != nil && !errors.Is(periodErr, pgx.ErrNoRows) {
		return fmt.Errorf("look up current subscription period: %w", periodErr)
	}
	now := time.Now().UTC()
	var curEndTime time.Time
	if currentPeriodEnd.Valid {
		curEndTime = currentPeriodEnd.Time.UTC()
	}

	periodStart, periodEnd, err := billing.CalculateRenewalPeriod(now, curEndTime, billingCycle)
	if err != nil {
		return fmt.Errorf("invalid billing cycle %q for order %d: %w", billingCycle, id, err)
	}

	if _, err := queries.PlatformUpdateOrderPaid(ctx, store.PlatformUpdateOrderPaidParams{TenantID: tenantID, ID: id}); err != nil {
		return err
	}
	if order.Kind == "new" && order.PlanCode.Valid {
		if _, taskErr := queries.CreateOrderOnboardingTask(ctx, store.CreateOrderOnboardingTaskParams{TenantID: tenantID, OrderID: pgtype.Int8{Int64: id, Valid: true}, Step: domainMail.StepDomainVerification, MaxAttempts: 5}); taskErr != nil {
			return fmt.Errorf("enqueue onboarding task for order %d: %w", id, taskErr)
		}
	}

	if planCode != "" {
		if _, err := queries.PlatformCancelActiveSubscriptions(ctx, tenantID); err != nil {
			return fmt.Errorf("cancel previous subscription: %w", err)
		}
		if _, err := queries.PlatformCreateSubscriptionForOrder(ctx, store.PlatformCreateSubscriptionForOrderParams{
			TenantID: tenantID, PlanID: planCode, PeriodStart: pgtype.Timestamptz{Time: periodStart, Valid: true},
			PeriodEnd: pgtype.Timestamptz{Time: periodEnd, Valid: true}, BillingCycle: billingCycle,
		}); err != nil {
			return fmt.Errorf("create subscription: %w", err)
		}
	}

	// The invoice is traceable to its order, so a bill stays reproducible after
	// a later plan price edit (M5 review 3.31).
	invoiceNo := fmt.Sprintf("INV-%d-%d-%d", tenantID, id, time.Now().UnixNano())
	if _, err := queries.PlatformCreateSettlementInvoice(ctx, store.PlatformCreateSettlementInvoiceParams{
		TenantID: tenantID, OrderID: pgtype.Int8{Int64: id, Valid: true}, Number: invoiceNo,
		PeriodStart: pgtype.Timestamptz{Time: periodStart, Valid: true}, PeriodEnd: pgtype.Timestamptz{Time: periodEnd, Valid: true},
		AmountCents: amount, Currency: currency,
	}); err != nil {
		return fmt.Errorf("issue invoice: %w", err)
	}

	// Record payment transaction
	if opts.Provider != "" {
		raw := opts.RawPayload
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		txnID := opts.ProviderTxnID
		if txnID == "" {
			txnID = fmt.Sprintf("MANUAL-%d-%d", id, time.Now().UnixNano())
		}
		_, createErr := store.New(tx).CreatePayment(ctx, store.CreatePaymentParams{
			TenantID:      tenantID,
			OrderID:       pgtype.Int8{Int64: id, Valid: true},
			Provider:      opts.Provider,
			ProviderTxnID: pgtype.Text{String: txnID, Valid: true},
			AmountCents:   amount,
			Currency:      currency,
			Status:        payment.StatusSucceeded,
			Raw:           raw,
		})
		if errors.Is(createErr, pgx.ErrNoRows) {
			// A duplicate can only be accepted when it already belongs to this
			// exact tenant/order/payment amount. Never turn a conflicting provider
			// transaction into a successful settlement for a second order.
			existingPayment, lookupErr := store.New(tx).PlatformGetPaymentByProviderTransaction(ctx, store.PlatformGetPaymentByProviderTransactionParams{
				Provider: opts.Provider, ProviderTxnID: pgtype.Text{String: txnID, Valid: true},
			})
			if lookupErr != nil || existingPayment.TenantID != tenantID || !existingPayment.OrderID.Valid || existingPayment.OrderID.Int64 != id || existingPayment.AmountCents != amount || !strings.EqualFold(existingPayment.Currency, currency) {
				return fmt.Errorf("%w: provider transaction conflicts with existing payment", ErrPaymentVerificationFailed)
			}
		} else if createErr != nil {
			// A failed payment insert must roll back the whole settlement:
			// the order would otherwise be marked paid without a durable
			// payment record.
			return fmt.Errorf("record payment: %w", createErr)
		}
	}

	if channelID.Valid {
		queries := store.New(tx)
		bps, err := queries.PlatformCommissionRate(ctx, channelID.Int64)
		if err != nil {
			return err
		}
		commission, err := distribution.CalculateCommission(amount, bps)
		if err != nil {
			return err
		}
		if _, err = queries.CreateCommission(ctx, store.CreateCommissionParams{
			ChannelID:   channelID.Int64,
			TenantID:    tenantID,
			OrderID:     id,
			AmountCents: commission,
			Bps:         bps,
			Status:      "pending",
		}); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}
