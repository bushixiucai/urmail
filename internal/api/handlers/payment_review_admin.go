package handlers

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// PaymentReviewHandler requires a deliberate operator choice for verified cash.
type PaymentReviewHandler struct{ Pool *db.Pool }

// List exposes verified receipts awaiting an explicit operator decision.
func (h *PaymentReviewHandler) List(c *gin.Context) {
	if h.Pool == nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	rows, err := store.New(h.Pool).PlatformPaymentReviewDetails(c)
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		out = append(out, gin.H{"id": row.ID, "tenant_id": row.TenantID, "order_id": row.OrderID, "payment_id": row.PaymentID, "reason": row.Reason, "status": row.Status, "created_at": row.CreatedAt, "received_amount_cents": row.ReceivedAmountCents, "received_amount_cents_exact": strconv.FormatInt(row.ReceivedAmountCents, 10), "received_currency": row.ReceivedCurrency, "expected_amount_cents": row.ExpectedAmountCents, "expected_amount_cents_exact": strconv.FormatInt(row.ExpectedAmountCents, 10), "expected_currency": row.ExpectedCurrency, "provider": row.Provider, "account_scope": row.AccountScope, "order_kind": row.OrderKind, "order_status": row.OrderStatus})
	}
	c.JSON(200, gin.H{"reviews": out})
}

// Resolve applies an audited fulfillment decision or records a refund request.
func (h *PaymentReviewHandler) Resolve(c *gin.Context) {
	uid, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if c.ShouldBindJSON(&req) != nil || len(strings.TrimSpace(req.Reason)) < 10 || len(req.Reason) > 1000 || (req.Action != "fulfill_after_prepaid" && req.Action != "request_refund") {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if h.Pool == nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	q := store.New(h.Pool)
	review, err := q.PlatformGetPaymentReview(c, id)
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 404, "PAYMENT_REVIEW_NOT_FOUND")
		return
	}
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	receipt, err := q.PlatformGetRefundPayment(c, store.PlatformGetRefundPaymentParams{TenantID: review.TenantID, ID: review.PaymentID})
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if req.Action == "fulfill_after_prepaid" {
		err = SettleOrderWithDetails(c.Request.Context(), h.Pool, review.OrderID, SettleOptions{Provider: receipt.Provider, AccountScope: receipt.AccountScope, ProviderTxnID: receipt.ProviderTxnID.String, AmountCents: receipt.AmountCents, Currency: receipt.Currency, ActorUserID: uid, AuditAction: "FULFILL receipt.manual_review", ManualReviewID: id, ReviewReason: req.Reason})
		if errors.Is(err, ErrPaymentVerificationFailed) {
			authError(c, 409, "PAYMENT_REVIEW_FULFILLMENT_CONFLICT")
			return
		}
		if err != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
		updated, loadErr := q.PlatformGetPaymentReview(c, id)
		if loadErr != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
		if updated.Status != "fulfilled" {
			authError(c, 409, "PAYMENT_REVIEW_FULFILLMENT_CONFLICT")
			return
		}
		c.JSON(200, gin.H{"review": updated})
		return
	}
	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(c) }()
	tq := store.New(tx)
	if err = tq.PlatformLockBillingTenant(c, review.TenantID); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	current, err := tq.PlatformGetPaymentReviewForUpdate(c, store.PlatformGetPaymentReviewForUpdateParams{TenantID: review.TenantID, ID: id})
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if current.Status != "open" {
		authError(c, 409, "PAYMENT_REVIEW_STATE_CONFLICT")
		return
	}
	requested, err := tq.PlatformCreateReviewRefund(c, store.PlatformCreateReviewRefundParams{TenantID: review.TenantID, ID: id, RefundNo: "REVIEW-" + strconv.FormatInt(id, 10), Reason: req.Reason})
	if err != nil {
		mapRefundApprovalError(c, err)
		return
	}
	detail, _ := json.Marshal(map[string]any{"action": "request_refund", "reason": req.Reason, "refund_id": requested.ID, "status": 202, "outcome": "awaiting_refund_approval"})
	if _, err = tq.CreateAuditLog(c, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: review.TenantID, Valid: true}, ActorUserID: pgtype.Int8{Int64: uid, Valid: true}, Action: "REQUEST receipt.refund", TargetType: pgtype.Text{String: "payment-reviews", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(id, 10), Valid: true}, Detail: detail}); err != nil {
		authError(c, 500, "AUDIT_UNAVAILABLE")
		return
	}
	if err = tx.Commit(c); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(202, gin.H{"refund": requested, "execution_status": "awaiting_approval"})
}
