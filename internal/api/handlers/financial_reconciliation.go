package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

// ErrFinancialPreviewStale refuses an absent, expired or changed review proof.
var ErrFinancialPreviewStale = errors.New("financial review preview expired or changed")

// FinancialReconciliation exposes scoped, redacted, frozen financial facts.
type FinancialReconciliation struct {
	Pool     *db.Pool
	Platform bool
}

type financialTerm struct {
	OrderID         int64           `json:"order_id"`
	BasisOrderID    *int64          `json:"basis_order_id"`
	Currency        string          `json:"currency"`
	Cycle           string          `json:"billing_cycle"`
	Start           time.Time       `json:"period_start"`
	BillingStart    time.Time       `json:"billing_period_start"`
	End             time.Time       `json:"period_end"`
	Calendar        json.RawMessage `json:"calendar_snapshot"`
	RevokedAt       *time.Time      `json:"revoked_at"`
	RevokedByRefund *int64          `json:"revoked_by_refund"`
}
type financialCash struct {
	ID                   int64      `json:"id"`
	PaymentID            *int64     `json:"payment_id"`
	EntitlementAction    string     `json:"entitlement_action"`
	EntitlementAppliedAt *time.Time `json:"entitlement_applied_at"`
	Amount               string     `json:"amount_cents_exact"`
	Currency             string     `json:"currency"`
	Status               string     `json:"status"`
}
type financialFact struct {
	Cycle       string          `json:"billing_cycle"`
	QuoteDigest *string         `json:"quote_digest"`
	ID          int64           `json:"id"`
	TenantID    int64           `json:"tenant_id"`
	Kind        string          `json:"kind"`
	Status      string          `json:"status"`
	Amount      string          `json:"amount_cents_exact"`
	Currency    string          `json:"currency"`
	Payments    []financialCash `json:"payments"`
	Refunds     []financialCash `json:"refunds"`
	Terms       []financialTerm `json:"terms"`
	Reviews     []struct {
		ID        int64  `json:"id"`
		PaymentID int64  `json:"payment_id"`
		Status    string `json:"status"`
	} `json:"reviews"`
	Checkout *struct {
		State       string `json:"state"`
		RestoreHold bool   `json:"restore_hold"`
	} `json:"checkout"`
}
type financialItem struct {
	Facts         json.RawMessage `json:"facts"`
	Discrepancies []string        `json:"discrepancies"`
}
type financialTotal struct {
	Orders    string `json:"ordered_cents_exact"`
	Confirmed string `json:"confirmed_cents_exact"`
	Refunded  string `json:"refunded_cents_exact"`
	Net       string `json:"net_cents_exact"`
}

func addExact(values map[string]*big.Int, key, raw string) bool {
	n, ok := new(big.Int).SetString(raw, 10)
	if !ok || n.Sign() < 0 {
		return false
	}
	if values[key] == nil {
		values[key] = new(big.Int)
	}
	values[key].Add(values[key], n)
	return true
}
func summarizeFinancial(raws [][]byte) ([]financialItem, map[string]financialTotal, error) {
	items := make([]financialItem, 0, len(raws))
	ordered, confirmed, refunded := map[string]*big.Int{}, map[string]*big.Int{}, map[string]*big.Int{}
	for _, raw := range raws {
		var f financialFact
		if json.Unmarshal(raw, &f) != nil || f.Currency == "" || !addExact(ordered, f.Currency, f.Amount) {
			return nil, nil, errors.New("invalid frozen financial facts")
		}
		issues := []string{}
		cash := new(big.Int)
		out := new(big.Int)
		foreign := false
		for _, p := range f.Payments {
			if p.Status == "succeeded" || p.Status == "refunded" {
				if !addExact(confirmed, p.Currency, p.Amount) {
					return nil, nil, errors.New("invalid confirmed receipt")
				}
				if p.Currency == f.Currency {
					n, _ := new(big.Int).SetString(p.Amount, 10)
					cash.Add(cash, n)
				} else {
					foreign = true
				}
			} else if p.Status != "failed" && p.Status != "refunded" {
				issues = append(issues, "payment_unknown")
			}
		}
		for _, r := range f.Refunds {
			if r.Status == "completed" {
				if !addExact(refunded, r.Currency, r.Amount) {
					return nil, nil, errors.New("invalid confirmed refund")
				}
				if r.Currency == f.Currency {
					n, _ := new(big.Int).SetString(r.Amount, 10)
					out.Add(out, n)
				} else {
					foreign = true
				}
			} else if r.Status != "rejected" && r.Status != "failed" {
				issues = append(issues, "refund_pending_or_unknown")
			}
		}
		amount, _ := new(big.Int).SetString(f.Amount, 10)
		if foreign || (cash.Sign() > 0 && cash.Cmp(amount) != 0) {
			issues = append(issues, "receipt_amount_or_currency_mismatch")
		}
		if f.Status == "paid" && amount.Sign() > 0 && cash.Sign() == 0 {
			issues = append(issues, "paid_without_confirmed_receipt")
		}
		if f.Status == "paid" && (f.Kind == "new" || f.Kind == "renew") && len(f.Terms) == 0 {
			issues = append(issues, "service_paid_without_term")
		}
		if out.Cmp(cash) > 0 {
			issues = append(issues, "refund_exceeds_confirmed_receipt")
		}
		if f.Checkout != nil && (f.Checkout.State == "uncertain" || f.Checkout.State == "creating" || f.Checkout.RestoreHold) {
			issues = append(issues, "checkout_unknown_or_held")
		}
		if f.QuoteDigest == nil || *f.QuoteDigest == "" {
			issues = append(issues, "historical_quote_unverified")
		}
		if len(f.Terms) > 1 {
			issues = append(issues, "duplicate_service_terms")
		}
		for _, term := range f.Terms {
			if term.Currency != f.Currency {
				issues = append(issues, "term_currency_mismatch")
			}
			normalize := func(s string) string {
				if s == "yearly" {
					return "annual"
				}
				return s
			}
			if normalize(term.Cycle) != normalize(f.Cycle) {
				issues = append(issues, "term_billing_cycle_mismatch")
			}
			if !term.End.After(term.Start) || term.BillingStart.IsZero() {
				issues = append(issues, "term_period_mismatch")
			}
			if term.BasisOrderID == nil {
				var spec billing.CalendarSpec
				switch {
				case len(term.Calendar) == 0 || string(term.Calendar) == "null" || string(term.Calendar) == "{}":
					issues = append(issues, "term_calendar_unverified")
				case json.Unmarshal(term.Calendar, &spec) != nil:
					issues = append(issues, "term_calendar_mismatch")
				case spec.Timezone == "" || spec.AnchorAt.IsZero():
					issues = append(issues, "term_calendar_unverified")
				default:
					anchor, e := spec.Anchor()
					if e != nil {
						issues = append(issues, "term_calendar_mismatch")
					} else if end, e := billing.AddAnchoredCycle(term.BillingStart, term.Cycle, anchor); e != nil || !end.Truncate(time.Microsecond).Equal(term.End.Truncate(time.Microsecond)) {
						issues = append(issues, "term_period_mismatch")
					}
				}
			}
			if term.RevokedAt != nil {
				found, wrong := false, false
				for _, r := range f.Refunds {
					if term.RevokedByRefund != nil && r.ID == *term.RevokedByRefund {
						found = true
						wrong = r.Status != "completed" || r.EntitlementAction == "none" || r.EntitlementAction == ""
					}
				}
				if wrong {
					issues = append(issues, "term_refund_revocation_mismatch")
				} else if !found {
					issues = append(issues, "term_refund_revocation_unverified")
				}
			}
			for _, r := range f.Refunds {
				if r.Status == "completed" && r.EntitlementAction != "" && r.EntitlementAction != "none" {
					if r.EntitlementAppliedAt == nil {
						issues = append(issues, "refund_entitlement_pending")
					} else if term.RevokedAt == nil {
						issues = append(issues, "term_refund_revocation_mismatch")
					}
				}
			}
		}
		for _, r := range f.Reviews {
			if r.Status == "open" {
				issues = append(issues, "manual_review_open")
				break
			}
		}
		items = append(items, financialItem{Facts: raw, Discrepancies: issues})
	}
	totals := map[string]financialTotal{}
	for _, set := range []map[string]*big.Int{ordered, confirmed, refunded} {
		for currency := range set {
			if _, ok := totals[currency]; ok {
				continue
			}
			get := func(m map[string]*big.Int) *big.Int {
				if m[currency] == nil {
					return new(big.Int)
				}
				return m[currency]
			}
			c, r := get(confirmed), get(refunded)
			totals[currency] = financialTotal{Orders: get(ordered).String(), Confirmed: c.String(), Refunded: r.String(), Net: new(big.Int).Sub(c, r).String()}
		}
	}
	return items, totals, nil
}

// Report returns a bounded page, or a complete bounded JSON evidence export.
func (h *FinancialReconciliation) Report(c *gin.Context) {
	actor, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "UNAUTHORIZED")
		return
	}
	tenant, ok := middleware.GetTenantID(c)
	if h.Platform {
		var e error
		tenant, e = strconv.ParseInt(c.Query("tenant_id"), 10, 64)
		ok = e == nil && tenant > 0
	} else if c.Query("tenant_id") != "" && c.Query("tenant_id") != strconv.FormatInt(tenant, 10) {
		authError(c, 403, "FORBIDDEN")
		return
	}
	from, e1 := parseTimeParam(c.Query("from"))
	to, e2 := parseTimeParam(c.Query("to"))
	if !ok || e1 != nil || e2 != nil || !to.After(from) || to.Sub(from) > 93*24*time.Hour {
		authError(c, 400, "FINANCIAL_WINDOW_INVALID")
		return
	}
	after := int64(0)
	if text := c.Query("after"); text != "" {
		var e error
		after, e = strconv.ParseInt(text, 10, 64)
		if e != nil || after < 0 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	}
	exporting := strings.HasSuffix(c.FullPath(), "/export") || strings.HasSuffix(c.Request.URL.Path, "/export")
	limit := 100
	if exporting {
		limit = 1000
		after = 0
	}
	if h.Pool == nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	var tx pgx.Tx
	var e error
	if h.Platform {
		tx, e = h.Pool.PlatformTx(c)
	} else {
		tx, e = h.Pool.UserTx(c, tenant, actor)
	}
	if e != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(c) }()
	q := store.New(tx)
	if e = q.PlatformFinancialReadBudget(c); e != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if h.Platform {
		_, e = q.PlatformLockRecurringAdminActor(c, actor)
	} else {
		_, e = q.PlatformLockFinancialTenantReader(c, store.PlatformLockFinancialTenantReaderParams{TenantID: tenant, ID: actor})
	}
	if e != nil {
		authError(c, 403, "FORBIDDEN")
		return
	}
	var raws [][]byte
	if h.Platform {
		raws, e = q.PlatformFinancialOrderFacts(c, store.PlatformFinancialOrderFactsParams{TenantID: tenant, OrderID: 0, FromAt: pgtype.Timestamptz{Time: from, Valid: true}, ToAt: pgtype.Timestamptz{Time: to, Valid: true}, AfterID: after, RowLimit: int32(limit + 1)})
	} else {
		raws, e = q.PlatformFinancialTenantOrderFacts(c, store.PlatformFinancialTenantOrderFactsParams{TenantID: tenant, OrderID: 0, FromAt: pgtype.Timestamptz{Time: from, Valid: true}, ToAt: pgtype.Timestamptz{Time: to, Valid: true}, AfterID: after, RowLimit: int32(limit + 1)})
	}

	if e != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	next := int64(0)
	if len(raws) > limit {
		if exporting {
			authError(c, 409, "FINANCIAL_EXPORT_LIMIT")
			return
		}
		raws = raws[:limit]
		var last financialFact
		_ = json.Unmarshal(raws[len(raws)-1], &last)
		next = last.ID
	}
	items, totals, e := summarizeFinancial(raws)
	if e != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	snapshot, _ := json.Marshal(items)
	if len(snapshot) > 8<<20 {
		authError(c, 409, "FINANCIAL_EXPORT_LIMIT")
		return
	}
	digest := sha256.Sum256(snapshot)
	detail, _ := json.Marshal(gin.H{"from": from, "to": to, "rows": len(items), "facts_sha256": hex.EncodeToString(digest[:]), "totals": totals, "complete_window": exporting || next == 0 && after == 0})
	action := "READ financial.reconciliation"
	if exporting {
		action = "EXPORT financial.reconciliation"
	}
	if h.Platform {
		if _, e = q.CreateAuditLog(c, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tenant, Valid: true}, ActorUserID: pgtype.Int8{Int64: actor, Valid: true}, Action: action, TargetType: pgtype.Text{String: "financial-facts", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(tenant, 10), Valid: true}, Detail: detail}); e != nil {
			authError(c, 500, "AUDIT_UNAVAILABLE")
			return
		}
	} else {
		route := "GET /api/v1/billing/reconciliation"
		if exporting {
			route += "/export"
		}
		target := "/api/v1/billing/reconciliation?from=" + from.Format(time.RFC3339) + "&to=" + to.Format(time.RFC3339) + "&facts_sha256=" + hex.EncodeToString(digest[:]) + "&rows=" + strconv.Itoa(len(items))
		if e = q.RecordTenantAudit(c, store.RecordTenantAuditParams{TenantID: tenant, UserID: actor, Action: route, Target: target, Status: 200}); e != nil {
			authError(c, 500, "AUDIT_UNAVAILABLE")
			return
		}
	}

	if e = tx.Commit(c); e != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	if exporting {
		c.Header("Content-Disposition", `attachment; filename="financial-facts.json"`)
	}
	c.JSON(200, gin.H{"schema_version": 1, "tenant_id": tenant, "from": from, "to": to, "evidence_at": time.Now().UTC(), "items": items, "totals": totals, "totals_scope": "returned_rows", "next_cursor": next, "facts_sha256": hex.EncodeToString(digest[:])})
}

func financialReviewFacts(ctx context.Context, q *store.Queries, tenant, order int64) ([]byte, error) {
	rows, e := q.PlatformFinancialOrderFacts(ctx, store.PlatformFinancialOrderFactsParams{TenantID: tenant, OrderID: order, RowLimit: 1})
	if e != nil {
		return nil, e
	}
	if len(rows) != 1 {
		return nil, ErrFinancialPreviewStale
	}
	return rows[0], nil
}
func financialFactsDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func financialReviewFulfillmentEligible(raw []byte, review int64) bool {
	var f financialFact
	if json.Unmarshal(raw, &f) != nil {
		return false
	}
	paymentID := int64(0)
	for _, r := range f.Reviews {
		if r.ID == review && r.Status == "open" {
			paymentID = r.PaymentID
		}
	}
	if paymentID == 0 {
		return false
	}
	confirmed := false
	for _, p := range f.Payments {
		if p.ID == paymentID && p.Status == "succeeded" && p.Amount == f.Amount && strings.EqualFold(p.Currency, f.Currency) {
			confirmed = true
		}
	}
	for _, r := range f.Refunds {
		if r.PaymentID != nil && *r.PaymentID == paymentID && r.Status != "rejected" && r.Status != "failed" {
			return false
		}
	}
	return confirmed
}

func validateFinancialReviewPreview(ctx context.Context, q *store.Queries, tenant, order, review, actor int64, decision, reason, digest string) error {
	if len(digest) != 64 {
		return ErrFinancialPreviewStale
	}
	if _, e := q.PlatformLockRecurringAdminActor(ctx, actor); e != nil {
		return ErrFinancialPreviewStale
	}
	if _, e := q.PlatformLockFinancialReviewMerchants(ctx, store.PlatformLockFinancialReviewMerchantsParams{TenantID: tenant, OrderID: order}); e != nil {
		return e
	}
	if _, e := q.PlatformLockFinancialReviewPayments(ctx, store.PlatformLockFinancialReviewPaymentsParams{TenantID: tenant, OrderID: pgtype.Int8{Int64: order, Valid: true}}); e != nil {
		return e
	}
	if _, e := q.PlatformLockFinancialReviewRefunds(ctx, store.PlatformLockFinancialReviewRefundsParams{TenantID: tenant, OrderID: order}); e != nil {
		return e
	}
	facts, e := financialReviewFacts(ctx, q, tenant, order)
	if e != nil {
		return e
	}
	if decision == "fulfill_after_prepaid" && !financialReviewFulfillmentEligible(facts, review) {
		return ErrPaymentVerificationFailed
	}
	valid, e := q.PlatformFinancialPreviewValid(ctx, store.PlatformFinancialPreviewValidParams{TenantID: pgtype.Int8{Int64: tenant, Valid: true}, ActorUserID: pgtype.Int8{Int64: actor, Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(review, 10), Valid: true}, Digest: digest, FactsSha256: financialFactsDigest(facts), Decision: decision, Reason: reason})
	if e != nil {
		return e
	}
	if !valid {
		return ErrFinancialPreviewStale
	}
	return nil
}

// Preview records an actor-bound decision against the current frozen facts.
func (h *PaymentReviewHandler) Preview(c *gin.Context) {
	actor, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	var in struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if e != nil || id < 1 || c.ShouldBindJSON(&in) != nil || len(strings.TrimSpace(in.Reason)) < 10 || len(in.Reason) > 1000 || (in.Action != "fulfill_after_prepaid" && in.Action != "request_refund") {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if h.Pool == nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	tx, e := h.Pool.PlatformTx(c)
	if e != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(c) }()
	q := store.New(tx)
	if _, e = q.PlatformLockRecurringAdminActor(c, actor); e != nil {
		authError(c, 403, "FORBIDDEN")
		return
	}
	review, e := q.PlatformGetPaymentReview(c, id)
	if e != nil {
		authError(c, 404, "PAYMENT_REVIEW_NOT_FOUND")
		return
	}
	if e = q.PlatformLockBillingTenant(c, review.TenantID); e != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	review, e = q.PlatformGetPaymentReview(c, id)
	if e != nil || review.Status != "open" {
		authError(c, 409, "PAYMENT_REVIEW_STATE_CONFLICT")
		return
	}
	facts, e := financialReviewFacts(c, q, review.TenantID, review.OrderID)
	if e != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if in.Action == "fulfill_after_prepaid" && !financialReviewFulfillmentEligible(facts, id) {
		authError(c, 409, "PAYMENT_REVIEW_FULFILLMENT_CONFLICT")
		return
	}
	nonce := make([]byte, 32)
	if _, e = rand.Read(nonce); e != nil {
		authError(c, 500, "INTERNAL_ERROR")
		return
	}
	digest := hex.EncodeToString(nonce)
	detail, _ := json.Marshal(gin.H{"digest": digest, "facts_sha256": financialFactsDigest(facts), "decision": in.Action, "reason": in.Reason})
	if _, e = q.CreateAuditLog(c, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: review.TenantID, Valid: true}, ActorUserID: pgtype.Int8{Int64: actor, Valid: true}, Action: "PREVIEW receipt.review", TargetType: pgtype.Text{String: "payment-reviews", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(id, 10), Valid: true}, Detail: detail}); e != nil {
		authError(c, 500, "AUDIT_UNAVAILABLE")
		return
	}
	if e = tx.Commit(c); e != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"digest": digest, "expires_at": time.Now().Add(10 * time.Minute), "facts_sha256": financialFactsDigest(facts), "facts": json.RawMessage(facts)})
}
