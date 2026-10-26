// Package refund executes explicitly approved original-route refunds.
package refund

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

var refundReference = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// Service never retries an unknown physical refund as a fresh payout.
type Service struct {
	Pool                          *db.Pool
	Registry                      *payment.Registry
	LegacyStripe                  *payment.Stripe
	Enabled, DevMode, Quarantined bool
	ProviderFor                   func(context.Context, store.PlatformGetRefundPaymentRow) (payment.Provider, error)
}

func (s *Service) provider(ctx context.Context, receipt store.PlatformGetRefundPaymentRow) (payment.Provider, error) {
	if s.ProviderFor != nil {
		return s.ProviderFor(ctx, receipt)
	}
	if receipt.AccountScope == "legacy" && receipt.Provider == payment.ProviderStripe && s.LegacyStripe != nil {
		return s.LegacyStripe, nil
	}
	if s.Registry == nil {
		return nil, payment.ErrUnsupportedOperation
	}
	_, provider, err := s.Registry.LoadScope(ctx, receipt.Provider, receipt.AccountScope)
	return provider, err
}
func (s *Service) active() bool { return s.Enabled && !s.DevMode && !s.Quarantined && s.Pool != nil }

// Step tries a bounded due batch, skipping unavailable providers without letting
// one merchant starve unrelated refunds. A physical payout is still claimed
// durably once before contacting its original provider.
func (s *Service) Step(ctx context.Context) (bool, error) {
	if !s.active() {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	q := store.New(s.Pool)
	candidates, err := q.PlatformListApprovedRefundCandidates(ctx)
	if err != nil {
		return false, err
	}
	var failures []error
	for _, next := range candidates {
		if err = ctx.Err(); err != nil {
			return false, errors.Join(append(failures, err)...)
		}
		dispatched, dispatchErr := s.dispatchCandidate(ctx, q, next)
		if dispatchErr != nil {
			failures = append(failures, dispatchErr)
		}
		if dispatched {
			return true, errors.Join(failures...)
		}
	}
	return false, errors.Join(failures...)
}

func (s *Service) dispatchCandidate(ctx context.Context, q *store.Queries, next store.Refunds) (bool, error) {
	receipt, err := q.PlatformGetRefundPayment(ctx, store.PlatformGetRefundPaymentParams{TenantID: next.TenantID, ID: next.PaymentID.Int64})
	if err != nil {
		flagErr := q.PlatformFlagApprovedRefund(ctx, store.PlatformFlagApprovedRefundParams{TenantID: next.TenantID, ID: next.ID, LastErrorCode: pgtype.Text{String: "receipt_unavailable", Valid: true}})
		return false, errors.Join(err, flagErr)
	}
	provider, err := s.provider(ctx, receipt)
	if err != nil {
		flagErr := q.PlatformFlagApprovedRefund(ctx, store.PlatformFlagApprovedRefundParams{TenantID: next.TenantID, ID: next.ID, LastErrorCode: pgtype.Text{String: "provider_unavailable", Valid: true}})
		return false, errors.Join(err, flagErr)
	}
	tx, err := s.Pool.PlatformTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tq := store.New(tx)
	if err = tq.PlatformLockBillingTenant(ctx, next.TenantID); err != nil {
		return false, err
	}
	claimed, err := tq.PlatformClaimRefundByID(ctx, store.PlatformClaimRefundByIDParams{TenantID: next.TenantID, ID: next.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		_ = tx.Rollback(context.Background())
		flagErr := q.PlatformFlagApprovedRefund(ctx, store.PlatformFlagApprovedRefundParams{TenantID: next.TenantID, ID: next.ID, LastErrorCode: pgtype.Text{String: "claim_rejected", Valid: true}})
		return false, errors.Join(err, flagErr)
	}
	if err = refundAudit(ctx, tq, claimed, "EXECUTE refund.dispatch", "processing"); err != nil {
		_ = tx.Rollback(context.Background())
		flagErr := q.PlatformFlagApprovedRefund(ctx, store.PlatformFlagApprovedRefundParams{TenantID: next.TenantID, ID: next.ID, LastErrorCode: pgtype.Text{String: "audit_unavailable", Valid: true}})
		return false, errors.Join(err, flagErr)
	}
	if err = tx.Commit(ctx); err != nil {
		// An ambiguous committed claim stays processing; the approved-only
		// predicate cannot make it executable again.
		flagErr := q.PlatformFlagApprovedRefund(ctx, store.PlatformFlagApprovedRefundParams{TenantID: next.TenantID, ID: next.ID, LastErrorCode: pgtype.Text{String: "claim_commit_unconfirmed", Valid: true}})
		return false, errors.Join(err, flagErr)
	}
	result, callErr := provider.Refund(ctx, payment.RefundParams{TenantID: claimed.TenantID, OrderID: claimed.OrderID, OrderNo: receipt.OrderNo, ProviderTxnID: receipt.ProviderTxnID.String, RefundNo: claimed.RefundNo, AmountCents: claimed.AmountCents, OriginalAmountCents: receipt.AmountCents, Currency: claimed.Currency, Reason: claimed.Reason})
	return true, s.record(ctx, claimed, receipt, result, callErr)
}

// Reconcile uses read-only provider queries, including stable merchant refund
// references when a successful response was lost before its ID was persisted.
func (s *Service) Reconcile(ctx context.Context) error {
	if !s.active() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	q := store.New(s.Pool)
	rows, err := q.PlatformListRefundsToReconcile(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, row := range rows {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		if !row.ApprovedBy.Valid {
			continue
		}
		// Advance the read-only reconciliation cursor even on provider/config
		// failures, so the same first fifty rows cannot starve later records.
		if deferErr := q.PlatformDeferRefundReconciliation(ctx, store.PlatformDeferRefundReconciliationParams{TenantID: row.TenantID, ID: row.ID}); deferErr != nil {
			failures = append(failures, deferErr)
			continue
		}
		if reconcileErr := s.reconcileCandidate(ctx, q, row); reconcileErr != nil {
			failures = append(failures, reconcileErr)
		}
	}
	return errors.Join(failures...)
}

func (s *Service) reconcileCandidate(ctx context.Context, q *store.Queries, row store.Refunds) error {
	receipt, err := q.PlatformGetRefundPayment(ctx, store.PlatformGetRefundPaymentParams{TenantID: row.TenantID, ID: row.PaymentID.Int64})
	if err != nil {
		return err
	}
	provider, err := s.provider(ctx, receipt)
	if err != nil {
		return err
	}
	query, ok := provider.(payment.RefundQueryProvider)
	if !ok {
		return payment.ErrUnsupportedOperation
	}
	session := ""
	if intent, e := q.PlatformGetCheckoutIntent(ctx, store.PlatformGetCheckoutIntentParams{TenantID: row.TenantID, OrderID: row.OrderID}); e == nil {
		session = intent.SessionID.String
	}
	result, queryErr := query.QueryRefund(ctx, payment.RefundQueryParams{RefundNo: row.RefundNo, OrderNo: receipt.OrderNo, ProviderRefundID: row.ProviderRefundNo.String, ProviderTxnID: receipt.ProviderTxnID.String, SessionID: session, ExpectedAmountCents: row.AmountCents, ExpectedCurrency: row.Currency})
	return s.record(ctx, row, receipt, result, queryErr)
}

func (s *Service) record(ctx context.Context, row store.Refunds, receipt store.PlatformGetRefundPaymentRow, result payment.RefundResult, upstreamErr error) error {
	state, code := "processing", ""
	if upstreamErr != nil {
		state = "uncertain"
		code = "provider_result_unknown"
	} else {
		switch payment.NormalizeRefundStatus(result.Status) {
		case payment.StatusSucceeded:
			if result.AmountCents != row.AmountCents || !strings.EqualFold(result.Currency, row.Currency) || result.ProviderRefundID == "" || (result.ProviderTxnID != "" && result.ProviderTxnID != receipt.ProviderTxnID.String) {
				state = "uncertain"
				code = "refund_verification_failed"
			} else {
				state = "completed"
			}
		case payment.StatusFailed:
			state = "failed"
		case "uncertain":
			state = "uncertain"
			code = "provider_requires_review"
		}
	}
	providerID := pgtype.Text{}
	if result.ProviderRefundID != "" {
		if !refundReference.MatchString(result.ProviderRefundID) {
			state = "uncertain"
			code = "refund_reference_invalid"
		} else {
			providerID = pgtype.Text{String: result.ProviderRefundID, Valid: true}
		}
	}
	tx, err := s.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(ctx, row.TenantID); err != nil {
		return err
	}
	current, err := q.PlatformGetRefundForUpdate(ctx, row.ID)
	if err != nil {
		return err
	}
	if current.Status == "completed" {
		return tx.Commit(ctx)
	}
	if current.Status != "processing" && current.Status != "uncertain" {
		return errors.New("refund is not awaiting provider reconciliation")
	}
	if _, err = q.PlatformRecordRefundProviderState(ctx, store.PlatformRecordRefundProviderStateParams{ID: row.ID, Status: state, ProviderRefundNo: providerID, LastErrorCode: pgtype.Text{String: code, Valid: code != ""}}); err != nil {
		return err
	}
	if err = refundAudit(ctx, q, current, "RESULT refund.provider", state); err != nil {
		return err
	}
	if err = q.PlatformEnqueueRefundResult(ctx, row.ID); err != nil {
		return err
	}
	if state == "completed" {
		if err = q.PlatformMarkCheckoutRefunded(ctx, store.PlatformMarkCheckoutRefundedParams{TenantID: row.TenantID, OrderID: row.OrderID}); err != nil {
			return err
		}
		if err = q.PlatformResolveRefundedReceiptReviews(ctx, row.ID); err != nil {
			return err
		}
		if err = q.PlatformAdjustCommissionForRefund(ctx, row.ID); err != nil {
			return err
		}
		if err = q.PlatformUpdateRefundedInvoice(ctx, store.PlatformUpdateRefundedInvoiceParams{TenantID: row.TenantID, SourcePaymentID: row.PaymentID}); err != nil {
			return err
		}
		if err = q.PlatformUpdateRefundedOrder(ctx, store.PlatformUpdateRefundedOrderParams{TenantID: row.TenantID, ID: row.OrderID, SourcePaymentID: row.PaymentID}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ApplyEntitlements honors both confirmed success and the approved effective
// time; unrelated future paid terms are never revoked by this action.
func (s *Service) ApplyEntitlements(ctx context.Context) error {
	if !s.active() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows, err := store.New(s.Pool).PlatformListRefundEntitlementsDue(ctx)
	if err != nil {
		return err
	}
	q := store.New(s.Pool)
	var failures []error
	for _, row := range rows {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		if deferErr := q.PlatformDeferRefundEntitlement(ctx, store.PlatformDeferRefundEntitlementParams{TenantID: row.TenantID, ID: row.ID}); deferErr != nil {
			failures = append(failures, deferErr)
			continue
		}
		if err = s.applyOne(ctx, row); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Service) applyOne(ctx context.Context, candidate store.Refunds) error {
	tx, err := s.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(ctx, candidate.TenantID); err != nil {
		return err
	}
	row, err := q.PlatformGetRefundForUpdate(ctx, candidate.ID)
	if err != nil {
		return err
	}
	if row.Status != "completed" || row.EntitlementAction == "none" || row.EntitlementAppliedAt.Valid || !row.EntitlementEffectiveAt.Valid || row.EntitlementEffectiveAt.Time.After(time.Now()) {
		return tx.Commit(ctx)
	}
	if !row.TargetTermOrderID.Valid {
		return errors.New("approved refund entitlement target missing")
	}
	if err = q.PlatformRevokeRefundTerm(ctx, store.PlatformRevokeRefundTermParams{TenantID: row.TenantID, OrderID: row.TargetTermOrderID.Int64, RevokedByRefund: pgtype.Int8{Int64: row.ID, Valid: true}}); err != nil {
		return err
	}
	if err = q.PlatformRecomputePaidEntitlementEnd(ctx, row.TenantID); err != nil {
		return err
	}
	if err = refundAudit(ctx, q, row, "APPLY refund.entitlement", "applied"); err != nil {
		return err
	}
	if err = q.PlatformMarkRefundEntitlementApplied(ctx, row.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Run dispatches approved refunds and reconciles uncertain provider outcomes.
func (s *Service) Run(ctx context.Context, logger *zap.Logger) {
	if !s.active() {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if _, err := s.Step(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("approved refund execution requires attention")
		}
		if err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("refund reconciliation requires attention")
		}
		if err := s.ApplyEntitlements(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("refund entitlement application requires attention")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func refundAudit(ctx context.Context, q *store.Queries, row store.Refunds, action, state string) error {
	detail, _ := json.Marshal(map[string]any{"actor_type": "worker", "approved_by": row.ApprovedBy.Int64, "result": state, "status": 200, "outcome": state, "entitlement_action": row.EntitlementAction})
	_, err := q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: row.TenantID, Valid: true}, Action: action, TargetType: pgtype.Text{String: "refunds", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(row.ID, 10), Valid: true}, Detail: detail})
	return err
}

// ReconcileOne is an explicit operator read of the original provider. It may
// record observed facts during restore quarantine, but never starts a refund
// or applies entitlement changes. Lost approval history requires confirmation.
func (s *Service) ReconcileOne(ctx context.Context, id, actorID int64, reason string, confirmObserved bool) (store.Refunds, error) {
	if s.Pool == nil || actorID < 1 || len(strings.TrimSpace(reason)) < 10 {
		return store.Refunds{}, errors.New("refund reconciliation requires an operator and reason")
	}
	q := store.New(s.Pool)
	allowed, err := q.CheckPlatformAdmin(ctx, actorID)
	if err != nil || !allowed {
		return store.Refunds{}, errors.New("refund reconciliation requires platform authorization")
	}
	row, err := q.PlatformGetRefundForUpdate(ctx, id)
	if err != nil {
		return row, err
	}
	if row.Status == "completed" || row.Status == "failed" || row.Status == "rejected" {
		return row, nil
	}
	receipt, err := q.PlatformGetRefundPayment(ctx, store.PlatformGetRefundPaymentParams{TenantID: row.TenantID, ID: row.PaymentID.Int64})
	if err != nil {
		return row, err
	}
	provider, err := s.provider(ctx, receipt)
	if err != nil {
		return row, err
	}
	query, ok := provider.(payment.RefundQueryProvider)
	if !ok {
		return row, payment.ErrUnsupportedOperation
	}
	session := ""
	if intent, e := q.PlatformGetCheckoutIntent(ctx, store.PlatformGetCheckoutIntentParams{TenantID: row.TenantID, OrderID: row.OrderID}); e == nil {
		session = intent.SessionID.String
	}
	result, queryErr := query.QueryRefund(ctx, payment.RefundQueryParams{RefundNo: row.RefundNo, OrderNo: receipt.OrderNo, ProviderRefundID: row.ProviderRefundNo.String, ProviderTxnID: receipt.ProviderTxnID.String, SessionID: session, ExpectedAmountCents: row.AmountCents, ExpectedCurrency: row.Currency})
	if queryErr != nil {
		return row, queryErr
	}
	if !row.ApprovedBy.Valid && !confirmObserved {
		return row, errors.New("observed refund requires explicit operator confirmation")
	}
	tx, err := s.Pool.PlatformTx(ctx)
	if err != nil {
		return row, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tq := store.New(tx)
	if err = tq.PlatformLockBillingTenant(ctx, row.TenantID); err != nil {
		return row, err
	}
	if !row.ApprovedBy.Valid {
		if _, err = tq.PlatformReconfirmObservedRefund(ctx, store.PlatformReconfirmObservedRefundParams{ID: id, ApprovedBy: pgtype.Int8{Int64: actorID, Valid: true}, ApprovalReason: pgtype.Text{String: "Observed provider refund: " + reason, Valid: true}}); err != nil {
			return row, err
		}
	} else if err = tq.PlatformPrepareRefundReconciliation(ctx, id); err != nil {
		return row, err
	}
	detail, _ := json.Marshal(map[string]any{"actor_type": "user", "mode": "provider_query_only", "confirm_observed": confirmObserved, "reason": reason, "status": 200, "outcome": "verified_provider_result"})
	if _, err = tq.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: row.TenantID, Valid: true}, ActorUserID: pgtype.Int8{Int64: actorID, Valid: true}, Action: "RECONCILE refund.observed", TargetType: pgtype.Text{String: "refunds", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(id, 10), Valid: true}, Detail: detail}); err != nil {
		return row, err
	}
	if err = tx.Commit(ctx); err != nil {
		return row, err
	}
	row, err = q.PlatformGetRefundForUpdate(ctx, id)
	if err != nil {
		return row, err
	}
	if err = s.record(ctx, row, receipt, result, nil); err != nil {
		return row, err
	}
	return q.PlatformGetRefundForUpdate(ctx, id)
}
