package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
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
	ActorUserID    int64
	AuditAction    string
	ManualReviewID int64
	ReviewReason   string
	PreviewDigest  string
	ReceiptOnly    bool
	AccountScope   string
	Provider       string
	ProviderTxnID  string
	EventID        string
	AmountCents    int64
	Currency       string
	RawPayload     []byte
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
	if opts.Provider != payment.ProviderManual && (opts.AmountCents <= 0 || opts.ProviderTxnID == "" || (opts.Currency != "CNY" && opts.Currency != "USD" && opts.Currency != "cny" && opts.Currency != "usd")) {
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
			AccountScope:    opts.AccountScope,
			ProviderEventID: opts.EventID,
			Payload:         payload,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Do not trust an event ID alone: only suppress a replay when its
			// persisted JSON payload is semantically identical to the original event.
			// A reused provider/event key with altered contents is a verification failure.
			existingEvent, lookupErr := queries.PlatformGetPaymentEventByProviderAndID(ctx, store.PlatformGetPaymentEventByProviderAndIDParams{
				Provider: opts.Provider, AccountScope: opts.AccountScope, ProviderEventID: opts.EventID,
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

	if err := store.New(tx).PlatformLockBillingTenant(ctx, tenantID); err != nil {
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
	intent, intentErr := queries.PlatformGetCheckoutIntent(ctx, store.PlatformGetCheckoutIntentParams{TenantID: tenantID, OrderID: id})
	if intentErr != nil && !errors.Is(intentErr, pgx.ErrNoRows) {
		return intentErr
	}
	manualRecovery := opts.ManualReviewID > 0
	if manualRecovery {
		if opts.ActorUserID <= 0 || len(strings.TrimSpace(opts.ReviewReason)) < 10 {
			return ErrPaymentVerificationFailed
		}
		allowed, checkErr := queries.CheckPlatformAdmin(ctx, opts.ActorUserID)
		if checkErr != nil || !allowed {
			return ErrPaymentVerificationFailed
		}
		review, reviewErr := queries.PlatformGetPaymentReviewForUpdate(ctx, store.PlatformGetPaymentReviewForUpdateParams{TenantID: tenantID, ID: opts.ManualReviewID})
		if reviewErr != nil || review.OrderID != id {
			return ErrPaymentVerificationFailed
		}
		if err = validateFinancialReviewPreview(ctx, queries, tenantID, id, opts.ManualReviewID, opts.ActorUserID, "fulfill_after_prepaid", opts.ReviewReason, opts.PreviewDigest); err != nil {
			return err
		}
		if review.Status == "fulfilled" {
			if err = queries.PlatformClearCheckoutRestoreHold(ctx, store.PlatformClearCheckoutRestoreHoldParams{TenantID: tenantID, OrderID: id}); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		if review.Status != "open" || order.Status == "paid" || order.Status == "refunded" || order.Status == "partially_refunded" {
			return ErrPaymentVerificationFailed
		}
		receipt, loadErr := queries.PlatformGetRefundPayment(ctx, store.PlatformGetRefundPaymentParams{TenantID: tenantID, ID: review.PaymentID})
		if loadErr != nil || receipt.Provider != opts.Provider || receipt.AccountScope != opts.AccountScope || receipt.ProviderTxnID.String != opts.ProviderTxnID {
			return ErrPaymentVerificationFailed
		}
		if receipt.AmountCents != order.AmountCents || !strings.EqualFold(receipt.Currency, order.Currency) {
			return ErrPaymentVerificationFailed
		}
		order.Status = "pending"
	}
	// Restore uncertainty survives instance-quarantine release. Only the
	// validated platform manual-review decision may grant subscription rights.
	if intentErr == nil && intent.RestoreHold && !manualRecovery {
		opts.ReceiptOnly = true
	}
	sameExistingReceipt := false
	var existingReceiptID int64
	if opts.Provider != payment.ProviderManual {
		scope := opts.AccountScope
		if scope == "" {
			scope = "legacy"
		}
		if intentErr == nil && (intent.Provider != opts.Provider || intent.AccountScope != scope) {
			return fmt.Errorf("%w: receipt merchant does not match checkout", ErrPaymentVerificationFailed)
		}
		if errors.Is(intentErr, pgx.ErrNoRows) && scope != "legacy" {
			return fmt.Errorf("%w: checkout merchant binding missing", ErrPaymentVerificationFailed)
		}
		if intentErr != nil && !errors.Is(intentErr, pgx.ErrNoRows) {
			return intentErr
		}
	}
	if opts.Provider != "" && opts.Provider != payment.ProviderManual && opts.ProviderTxnID != "" {
		existingPayment, lookupErr := queries.PlatformGetPaymentByProviderTransaction(ctx, store.PlatformGetPaymentByProviderTransactionParams{
			Provider: opts.Provider, AccountScope: opts.AccountScope, ProviderTxnID: pgtype.Text{String: opts.ProviderTxnID, Valid: true},
		})
		if lookupErr == nil {
			sameExistingReceipt = true
			existingReceiptID = existingPayment.ID
			if existingPayment.TenantID != tenantID || !existingPayment.OrderID.Valid || existingPayment.OrderID.Int64 != id || existingPayment.AmountCents != opts.AmountCents || !strings.EqualFold(existingPayment.Currency, opts.Currency) {
				return fmt.Errorf("%w: provider transaction is already attached to another order", ErrPaymentVerificationFailed)
			}
		} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return fmt.Errorf("lookup provider transaction: %w", lookupErr)
		}
	}
	if sameExistingReceipt && !manualRecovery {
		if review, reviewErr := queries.PlatformReviewByReceipt(ctx, store.PlatformReviewByReceiptParams{TenantID: tenantID, PaymentID: existingReceiptID}); reviewErr == nil && review.Status != "fulfilled" {
			if auditErr := settlementAudit(ctx, queries, tenantID, id, opts, "manual_review_retained"); auditErr != nil {
				return auditErr
			}
			return tx.Commit(ctx)
		} else if reviewErr != nil && !errors.Is(reviewErr, pgx.ErrNoRows) {
			return reviewErr
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
		return recordManualReceipt(ctx, tx, order, opts, "receipt_amount_mismatch")
	}
	if opts.Provider == payment.ProviderManual && opts.AmountCents > 0 && opts.AmountCents != amount {
		return fmt.Errorf("%w: expected amount %d, got %d", ErrPaymentVerificationFailed, amount, opts.AmountCents)
	}
	if opts.Currency != "" && !strings.EqualFold(opts.Currency, currency) {
		if opts.Provider != payment.ProviderManual {
			return recordManualReceipt(ctx, tx, order, opts, "receipt_currency_mismatch")
		}
		return fmt.Errorf("%w: expected currency %s, got %s", ErrPaymentVerificationFailed, currency, opts.Currency)
	}

	if opts.ReceiptOnly && (!sameExistingReceipt || (order.Status != "paid" && order.Status != "partially_refunded" && order.Status != "refunded")) {
		return recordManualReceipt(ctx, tx, order, opts, "restore_reconciliation")
	}
	if order.Status == "paid" || order.Status == "partially_refunded" || order.Status == "refunded" {
		if opts.Provider != payment.ProviderManual && !sameExistingReceipt {
			return recordManualReceipt(ctx, tx, order, opts, "additional_receipt_on_paid_order")
		}
		return tx.Commit(ctx)
	}
	if order.Status != "pending" {
		if opts.Provider == payment.ProviderManual {
			return ErrOrderNotFound
		}
		return recordManualReceipt(ctx, tx, order, opts, "late_receipt_"+order.Status)
	}

	if _, quoteErr := queries.PlatformGetOrderQuote(ctx, id); quoteErr == nil {
		if authorityErr := verifyPersistedOrderAuthority(ctx, queries, order); authorityErr != nil {
			if opts.Provider != payment.ProviderManual {
				return recordManualReceipt(ctx, tx, order, opts, "order_authority_invalid")
			}
			return fmt.Errorf("%w: order pricing authority missing or changed", ErrPaymentVerificationFailed)
		}
	} else if errors.Is(quoteErr, pgx.ErrNoRows) {
		trustedLegacy := opts.Provider == payment.ProviderManual && order.AmountCents > 0
		if trustedLegacy && opts.ActorUserID > 0 {
			trustedLegacy, err = queries.CheckPlatformAdmin(ctx, opts.ActorUserID)
			if err != nil {
				return err
			}
		}
		if !trustedLegacy {
			if opts.Provider != payment.ProviderManual {
				return recordManualReceipt(ctx, tx, order, opts, "legacy_order_requires_pricing_review")
			}
			return ErrPaymentVerificationFailed
		}
	} else {
		return quoteErr
	}

	// Compute accurate period using domain pricing rules (preserves remaining
	// active days). An unknown cycle can only come from a data integrity bug
	// (the column is CHECK-constrained), and silently billing a year-long order
	// as one month would undercharge the tenant: fail the settlement instead so
	// the provider retries and the operator sees the problem (M5 review 3.30).
	current, periodErr := queries.PlatformGetSubscriptionForSettlement(ctx, tenantID)
	if periodErr != nil && !errors.Is(periodErr, pgx.ErrNoRows) {
		return fmt.Errorf("look up current subscription period: %w", periodErr)
	}

	currentCode := current.PlanID
	var currentEffective store.Plans
	if periodErr == nil {
		effective, effectiveErr := queries.GetEffectivePlan(ctx, tenantID)
		if effectiveErr != nil {
			return effectiveErr
		}
		currentEffective = effective
		currentCode = effective.Code
	}
	// A signed purchase describes the subscription version the customer saw.
	// Money received against a stale intent must never erase newer paid rights.
	if quoted, quoteErr := queries.PlatformGetOrderQuote(ctx, id); quoteErr == nil {
		var agreed billing.Quote
		if json.Unmarshal(quoted.Payload, &agreed) != nil {
			return errors.New("invalid persisted billing quote")
		}
		currentVersion := int64(0)
		if periodErr == nil {
			currentVersion = current.ID
		}
		compatibleRenewal := order.Kind == "renew" && periodErr == nil && currentCode == agreed.PlanCode
		if order.Kind == "renew" {
			future, futureErr := queries.LastFuturePaidPlan(ctx, tenantID)
			if futureErr != nil && !errors.Is(futureErr, pgx.ErrNoRows) {
				return futureErr
			}
			if futureErr == nil {
				compatibleRenewal = future.PlanCode == agreed.PlanCode
			}
		}
		if !billing.QuoteSourceMatches(agreed.CurrentEntitlementSource, current.EntitlementSource) && !manualRecovery {
			if opts.Provider == payment.ProviderManual {
				return fmt.Errorf("%w: entitlement source changed since quote", ErrPaymentVerificationFailed)
			}
			return recordManualReceipt(ctx, tx, order, opts, "entitlement_source_changed")
		}
		if agreed.SubscriptionVersion != currentVersion && !compatibleRenewal && (!manualRecovery || order.Kind == "upgrade") {
			if opts.Provider == payment.ProviderManual {
				return fmt.Errorf("%w: subscription changed since quote", ErrPaymentVerificationFailed)
			}
			return recordManualReceipt(ctx, tx, order, opts, "subscription_changed")
		}
	} else if !errors.Is(quoteErr, pgx.ErrNoRows) {
		return quoteErr
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	var curEndTime time.Time
	if periodErr == nil && currentCode != "free" && current.Status != "trialing" && (order.Kind == "renew" || order.Kind == "downgrade") && current.PeriodEnd.Valid {
		curEndTime = current.PeriodEnd.Time.UTC()
	}

	if manualRecovery && order.Kind != "upgrade" && periodErr == nil && currentCode != "free" && current.Status != "trialing" && current.PeriodEnd.Valid && current.PeriodEnd.Time.After(now) {
		curEndTime = current.PeriodEnd.Time.UTC()
	}
	var periodStart, periodEnd time.Time
	switch billingCycle {
	case billing.CycleMonthly, billing.CycleQuarterly, billing.CycleAnnual:
	default:
		if opts.Provider != payment.ProviderManual && !manualRecovery {
			return recordManualReceipt(ctx, tx, order, opts, "billing_cycle_invalid")
		}
		return ErrPaymentVerificationFailed
	}

	if order.Kind == "upgrade" {
		term, termErr := queries.GetCurrentSubscriptionTerm(ctx, tenantID)
		if termErr != nil {
			if errors.Is(termErr, pgx.ErrNoRows) && opts.Provider != payment.ProviderManual {
				return recordManualReceipt(ctx, tx, order, opts, "upgrade_period_expired")
			}
			return termErr
		}
		basis := term.OrderID
		if term.BasisOrderID.Valid {
			basis = term.BasisOrderID.Int64
		}
		pending, pendingErr := queries.PendingRefundForTerm(ctx, store.PendingRefundForTermParams{TenantID: tenantID, TargetTermOrderID: pgtype.Int8{Int64: basis, Valid: true}})
		if pendingErr != nil {
			return pendingErr
		}
		if pending {
			if opts.Provider != payment.ProviderManual {
				return recordManualReceipt(ctx, tx, order, opts, "entitlement_refund_pending")
			}
			return ErrPaymentVerificationFailed
		}
	}

	entitlementStart := periodStart
	if !curEndTime.IsZero() && curEndTime.After(now) && current.PeriodStart.Valid && !current.PeriodStart.Time.After(now) {
		entitlementStart = current.PeriodStart.Time
	}
	var snapshot []byte
	var documentSnapshot []byte
	var quoteBase int64
	var quoteDomainID int64
	var agreedQuote *billing.Quote
	if quoted, quoteErr := queries.PlatformGetOrderQuote(ctx, id); quoteErr == nil {
		snapshot = quoted.PlanSnapshot
		documentSnapshot = quoted.DocumentSnapshot
		var agreed billing.Quote
		if json.Unmarshal(quoted.Payload, &agreed) != nil {
			return errors.New("invalid persisted billing quote")
		}
		agreedQuote = &agreed
		quoteDomainID = agreed.DomainID
		quoteBase = agreed.PlanAmountCents
		if quoteBase == 0 {
			quoteBase = agreed.BaseAmountCents
		}
	} else if !errors.Is(quoteErr, pgx.ErrNoRows) {
		return quoteErr
	}

	billingStart := periodStart
	var basisOrderID pgtype.Int8
	if order.Kind == "upgrade" {
		term, termErr := queries.GetCurrentSubscriptionTerm(ctx, tenantID)
		if termErr != nil || !term.PeriodEnd.Time.After(now) {
			return fmt.Errorf("%w: upgrade period no longer current", ErrPaymentVerificationFailed)
		}
		quoted, quoteErr := queries.PlatformGetOrderQuote(ctx, id)
		if quoteErr != nil {
			return quoteErr
		}
		var agreed billing.Quote
		if json.Unmarshal(quoted.Payload, &agreed) != nil || agreed.SubscriptionVersion != current.ID {
			return fmt.Errorf("%w: subscription changed since quote", ErrPaymentVerificationFailed)
		}
		periodStart = now
		periodEnd = term.PeriodEnd.Time
		billingStart = term.BillingPeriodStart.Time
		basisOrderID = term.BasisOrderID
		if !basisOrderID.Valid {
			basisOrderID = pgtype.Int8{Int64: term.OrderID, Valid: true}
		}
		entitlementStart = current.PeriodStart.Time
	}
	tenant, tenantErr := queries.GetTenantByID(ctx, tenantID)
	if tenantErr != nil {
		return tenantErr
	}
	if tenant.Status != "active" && tenant.Status != "pending" {
		if manualRecovery || opts.Provider == payment.ProviderManual {
			return ErrPaymentVerificationFailed
		}
		return recordManualReceipt(ctx, tx, order, opts, "tenant_not_available")
	}
	if quoteDomainID > 0 {
		domain, domainErr := queries.GetDomainByID(ctx, store.GetDomainByIDParams{TenantID: tenantID, ID: quoteDomainID})
		if domainErr != nil || domain.Status != "active" || !domain.VerifiedAt.Valid || domain.VerifiedAt.Time.After(now) {
			if manualRecovery || opts.Provider == payment.ProviderManual {
				return ErrPaymentVerificationFailed
			}
			return recordManualReceipt(ctx, tx, order, opts, "domain_ownership_not_current")
		}
	}
	if manualRecovery && len(snapshot) == 0 {
		return ErrPaymentVerificationFailed
	}
	if len(snapshot) > 0 {
		fits, fitErr := queries.PlanSnapshotFitsResources(ctx, store.PlanSnapshotFitsResourcesParams{TenantID: tenantID, PlanSnapshot: snapshot})
		if fitErr != nil {
			return fitErr
		}
		if !fits {
			if manualRecovery || opts.Provider == payment.ProviderManual {
				return fmt.Errorf("%w: plan capacity exceeded", ErrPaymentVerificationFailed)
			}
			return recordManualReceipt(ctx, tx, order, opts, "plan_capacity_exceeded")
		}
	}
	continuousCalendar := (!curEndTime.IsZero() && curEndTime.After(now)) || order.Kind == "upgrade"
	saleCalendar, calendarErr := settlementBillingCalendar(ctx, queries, tenantID, id, now, continuousCalendar, manualRecovery, agreedQuote)
	if calendarErr != nil {
		if errors.Is(calendarErr, errBillingCalendarReview) || errors.Is(calendarErr, errBillingCalendarChanged) || errors.Is(calendarErr, billing.ErrInvalidCalendarAnchor) {
			if manualRecovery || opts.Provider == payment.ProviderManual {
				return fmt.Errorf("%w: %w", ErrPaymentVerificationFailed, calendarErr)
			}
			return recordManualReceipt(ctx, tx, order, opts, "billing_calendar_requires_review")
		}
		return calendarErr
	}
	if order.Kind != "upgrade" {
		anchor, anchorErr := saleCalendar.Anchor()
		if anchorErr != nil {
			return anchorErr
		}
		periodStart, periodEnd, err = billing.CalculateAnchoredRenewalPeriod(now, curEndTime, billingCycle, anchor)
		if err != nil {
			return err
		}
		billingStart = periodStart
		entitlementStart = periodStart
		if !curEndTime.IsZero() && curEndTime.After(now) && current.PeriodStart.Valid && !current.PeriodStart.Time.After(now) {
			entitlementStart = current.PeriodStart.Time
		}
	}
	calendarSnapshot, _ := json.Marshal(saleCalendar)
	if manualRecovery {
		restored, restoreErr := queries.PlatformRestoreReviewOrderPending(ctx, store.PlatformRestoreReviewOrderPendingParams{TenantID: tenantID, ID: id})
		if restoreErr != nil {
			return restoreErr
		}
		if restored != 1 {
			return ErrPaymentVerificationFailed
		}
	}
	if _, err := queries.PlatformUpdateOrderPaid(ctx, store.PlatformUpdateOrderPaidParams{TenantID: tenantID, ID: id}); err != nil {
		return err
	}

	if order.Kind == "new" && order.PlanCode.Valid {
		if _, taskErr := queries.CreateOrderOnboardingTask(ctx, store.CreateOrderOnboardingTaskParams{TenantID: tenantID, OrderID: pgtype.Int8{Int64: id, Valid: true}, DomainID: pgtype.Int8{Int64: quoteDomainID, Valid: quoteDomainID > 0}, Step: domainMail.StepDomainVerification, MaxAttempts: 5}); taskErr != nil {
			return fmt.Errorf("enqueue onboarding task for order %d: %w", id, taskErr)
		}
	}

	if len(snapshot) > 0 {
		if err := queries.PlatformRecordSubscriptionTerm(ctx, store.PlatformRecordSubscriptionTermParams{OrderID: id, TenantID: tenantID, BasisOrderID: basisOrderID, PeriodStart: pgtype.Timestamptz{Time: periodStart, Valid: true}, PeriodEnd: pgtype.Timestamptz{Time: periodEnd, Valid: true}, PlanSnapshot: snapshot, BaseAmountCents: quoteBase, Currency: currency, BillingCycle: billingCycle, BillingPeriodStart: pgtype.Timestamptz{Time: billingStart, Valid: true}, CalendarSnapshot: calendarSnapshot}); err != nil {
			return err
		}
	}
	currentSnapshot := snapshot
	summaryPlanCode := planCode
	summaryCycle := billingCycle
	if !curEndTime.IsZero() && curEndTime.After(now) {
		currentSnapshot, _ = json.Marshal(newPlanResponse(currentEffective))
		summaryPlanCode = currentCode
		summaryCycle = current.BillingCycle
		if term, termErr := queries.GetCurrentSubscriptionTerm(ctx, tenantID); termErr == nil {
			summaryCycle = term.BillingCycle
		}
	}

	if planCode != "" {
		if _, err := queries.PlatformCancelActiveSubscriptions(ctx, tenantID); err != nil {
			return fmt.Errorf("cancel previous subscription: %w", err)
		}
		if _, err := queries.PlatformCreateSubscriptionForOrder(ctx, store.PlatformCreateSubscriptionForOrderParams{
			TenantID: tenantID, PlanID: summaryPlanCode, PeriodStart: pgtype.Timestamptz{Time: entitlementStart, Valid: true},
			PeriodEnd: pgtype.Timestamptz{Time: periodEnd, Valid: true}, BillingCycle: summaryCycle, PlanSnapshot: currentSnapshot,
		}); err != nil {
			return fmt.Errorf("create subscription: %w", err)

		}
	}

	// The invoice is traceable to its order, so a bill stays reproducible after
	// a later plan price edit (M5 review 3.31).
	invoiceNo := fmt.Sprintf("INV-%d-%d-%d", tenantID, id, time.Now().UnixNano())
	invoice, invoiceErr := queries.PlatformCreateSettlementInvoice(ctx, store.PlatformCreateSettlementInvoiceParams{
		TenantID: tenantID, OrderID: pgtype.Int8{Int64: id, Valid: true}, Number: invoiceNo,
		PeriodStart: pgtype.Timestamptz{Time: periodStart, Valid: true}, PeriodEnd: pgtype.Timestamptz{Time: periodEnd, Valid: true},
		AmountCents: amount, Currency: currency,
	})
	if invoiceErr != nil {
		return fmt.Errorf("issue invoice: %w", invoiceErr)
	}
	if len(documentSnapshot) == 0 || string(documentSnapshot) == "{}" {
		var captureErr error
		documentSnapshot, captureErr = CaptureInvoiceDocumentSnapshot(ctx, queries, tenantID, amount, currency, "observed_at_settlement")
		if captureErr != nil {
			return captureErr
		}
	}
	frozen, freezeErr := queries.PlatformFreezeInvoiceDocumentSnapshot(ctx, store.PlatformFreezeInvoiceDocumentSnapshotParams{TenantID: tenantID, ID: invoice.ID, DocumentSnapshot: documentSnapshot})
	if freezeErr != nil {
		return freezeErr
	}
	if frozen != 1 {
		return errors.New("invoice document snapshot could not be frozen")
	}

	// Record payment transaction
	var recordedPaymentID int64
	if opts.Provider != "" && amount > 0 {
		raw := opts.RawPayload
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		txnID := opts.ProviderTxnID
		if txnID == "" {
			txnID = fmt.Sprintf("MANUAL-%d-%d", id, time.Now().UnixNano())
		}
		recordedPayment, createErr := store.New(tx).CreatePayment(ctx, store.CreatePaymentParams{
			TenantID:      tenantID,
			OrderID:       pgtype.Int8{Int64: id, Valid: true},
			Provider:      opts.Provider,
			AccountScope:  opts.AccountScope,
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
				Provider: opts.Provider, AccountScope: opts.AccountScope, ProviderTxnID: pgtype.Text{String: txnID, Valid: true},
			})
			if lookupErr != nil || existingPayment.TenantID != tenantID || !existingPayment.OrderID.Valid || existingPayment.OrderID.Int64 != id || existingPayment.AmountCents != amount || !strings.EqualFold(existingPayment.Currency, currency) {
				return fmt.Errorf("%w: provider transaction conflicts with existing payment", ErrPaymentVerificationFailed)
			}
			recordedPayment = existingPayment
		} else if createErr != nil {
			// A failed payment insert must roll back the whole settlement:
			// the order would otherwise be marked paid without a durable
			// payment record.
			return fmt.Errorf("record payment: %w", createErr)
		}
		recordedPaymentID = recordedPayment.ID
		if recordedPaymentID > 0 {
			if bindErr := queries.PlatformBindInvoicePayment(ctx, store.PlatformBindInvoicePaymentParams{TenantID: tenantID, OrderID: pgtype.Int8{Int64: id, Valid: true}, SourcePaymentID: pgtype.Int8{Int64: recordedPaymentID, Valid: true}}); bindErr != nil {
				return bindErr
			}
		}
	}

	if channelID.Valid && amount > 0 {
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
			ChannelID:       channelID.Int64,
			SourcePaymentID: pgtype.Int8{Int64: recordedPaymentID, Valid: recordedPaymentID > 0},
			TenantID:        tenantID,
			OrderID:         id,
			AmountCents:     commission,
			Bps:             bps,
			Status:          "pending",
		}); err != nil {
			return err
		}
	}

	if manualRecovery {
		if err = queries.PlatformClearCheckoutRestoreHold(ctx, store.PlatformClearCheckoutRestoreHoldParams{TenantID: tenantID, OrderID: id}); err != nil {
			return err
		}
		if _, resolveErr := queries.PlatformResolvePaymentReviewFulfilled(ctx, store.PlatformResolvePaymentReviewFulfilledParams{TenantID: tenantID, ID: opts.ManualReviewID, ResolutionReason: pgtype.Text{String: opts.ReviewReason, Valid: true}, ResolvedBy: pgtype.Int8{Int64: opts.ActorUserID, Valid: true}}); resolveErr != nil {
			return resolveErr
		}
	}
	if err = queries.PlatformMarkCheckoutReceived(ctx, store.PlatformMarkCheckoutReceivedParams{TenantID: tenantID, OrderID: id}); err != nil {
		return err
	}
	// BILL-004: a successful settlement supersedes the previous billing
	// cycle. Cancel the still-pending renewal attempts of the old cycle in
	// the same transaction so they can neither retry nor notify again; the
	// claim query additionally refuses periods that no longer match, which
	// closes the enqueue/claim race window.
	if err = queries.PlatformCancelSupersededRenewalAttempts(ctx, tenantID); err != nil {
		return err
	}
	if err = settlementAudit(ctx, queries, tenantID, id, opts, "settled"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// recordManualReceipt commits the verified funds and an unresolved operator task
// without activating entitlements, issuing an invoice, or initiating a refund.
func recordManualReceipt(ctx context.Context, tx pgx.Tx, order store.PlatformGetOrderForSettlementRow, opts SettleOptions, reason string) error {
	q := store.New(tx)
	receiptCurrency := strings.ToUpper(opts.Currency)
	if receiptCurrency == "" {
		receiptCurrency = order.Currency
	}
	raw := opts.RawPayload
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if opts.ProviderTxnID == "" {
		return fmt.Errorf("%w: receipt transaction ID missing", ErrPaymentVerificationFailed)
	}
	received, err := q.CreatePayment(ctx, store.CreatePaymentParams{TenantID: order.TenantID, OrderID: pgtype.Int8{Int64: order.ID, Valid: true}, Provider: opts.Provider, AccountScope: opts.AccountScope, ProviderTxnID: pgtype.Text{String: opts.ProviderTxnID, Valid: true}, AmountCents: opts.AmountCents, Currency: receiptCurrency, Status: payment.StatusSucceeded, Raw: raw})
	if errors.Is(err, pgx.ErrNoRows) {
		received, err = q.PlatformGetPaymentByProviderTransaction(ctx, store.PlatformGetPaymentByProviderTransactionParams{Provider: opts.Provider, AccountScope: opts.AccountScope, ProviderTxnID: pgtype.Text{String: opts.ProviderTxnID, Valid: true}})
	}
	if err != nil {
		return err
	}
	if received.TenantID != order.TenantID || !received.OrderID.Valid || received.OrderID.Int64 != order.ID || received.AmountCents != opts.AmountCents || !strings.EqualFold(received.Currency, receiptCurrency) {
		return ErrPaymentVerificationFailed
	}
	if err = q.PlatformRecordPaymentManualReview(ctx, store.PlatformRecordPaymentManualReviewParams{TenantID: order.TenantID, OrderID: order.ID, PaymentID: received.ID, Reason: reason}); err != nil {
		return err
	}
	if err = q.PlatformMarkCheckoutReceived(ctx, store.PlatformMarkCheckoutReceivedParams{TenantID: order.TenantID, OrderID: order.ID}); err != nil {
		return err
	}
	if err = settlementAudit(ctx, q, order.TenantID, order.ID, opts, "manual_review"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func settlementAudit(ctx context.Context, q *store.Queries, tenantID, orderID int64, opts SettleOptions, outcome string) error {
	actorType := "provider"
	if opts.Provider == payment.ProviderManual {
		actorType = "system"
	}
	if opts.ActorUserID > 0 {
		actorType = "user"
	}
	action := opts.AuditAction
	if action == "" {
		action = "SETTLE order.payment"
	}
	detail, _ := json.Marshal(map[string]any{"actor_type": actorType, "provider": opts.Provider, "account_scope": opts.AccountScope, "outcome": outcome, "status": 200})
	_, err := q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tenantID, Valid: true}, ActorUserID: pgtype.Int8{Int64: opts.ActorUserID, Valid: opts.ActorUserID > 0}, Action: action, TargetType: pgtype.Text{String: "orders", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(orderID, 10), Valid: true}, Detail: detail})
	return err
}
