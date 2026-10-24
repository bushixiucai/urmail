package handlers

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// RefundAdminHandler separates platform approval from tenant requests.
type RefundAdminHandler struct{ Store *store.Queries }

// List returns refunds requiring platform oversight.
func (h *RefundAdminHandler) List(c *gin.Context) {
	q := requestStore(c, h.Store)
	if q == nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	rows, err := q.PlatformListRefundsForApproval(c, c.Query("status"))
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"refunds": rows})
}

// Approve records a trusted operator decision before any refund dispatch.
func (h *RefundAdminHandler) Approve(c *gin.Context) {
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
		Decision          string `json:"decision"`
		AmountCents       int64  `json:"amount_cents"`
		Reason            string `json:"reason"`
		EntitlementAction string `json:"entitlement_action"`
		EffectiveAt       string `json:"effective_at"`
		TermOrderID       int64  `json:"term_order_id"`
	}
	if c.ShouldBindJSON(&req) != nil || len(strings.TrimSpace(req.Reason)) < 10 || len(req.Reason) > 1000 || (req.Decision != "approve" && req.Decision != "reject") {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	q := requestStore(c, h.Store)
	if q == nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	tenant, err := q.PlatformRefundTenant(c, id)
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 404, "REFUND_NOT_FOUND")
		return
	}
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if err = q.PlatformLockBillingTenant(c, tenant); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if req.Decision == "reject" {
		row, rejectErr := q.PlatformRejectRefund(c, store.PlatformRejectRefundParams{ID: id, ApprovedBy: pgtype.Int8{Int64: uid, Valid: true}, ApprovalReason: pgtype.Text{String: req.Reason, Valid: true}})
		if rejectErr != nil {
			mapRefundApprovalError(c, rejectErr)
			return
		}
		c.JSON(200, gin.H{"refund": row})
		return
	}
	if req.AmountCents <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if req.EntitlementAction == "" {
		req.EntitlementAction = "none"
	}
	var effective pgtype.Timestamptz
	var target pgtype.Int8
	if req.EntitlementAction != "none" {
		if req.EntitlementAction != "revoke_future" && req.EntitlementAction != "terminate_current" {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		when, parseErr := time.Parse(time.RFC3339, req.EffectiveAt)
		if parseErr != nil || req.TermOrderID < 1 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		effective = pgtype.Timestamptz{Time: when, Valid: true}
		target = pgtype.Int8{Int64: req.TermOrderID, Valid: true}
	}
	row, err := q.PlatformApproveRefund(c, store.PlatformApproveRefundParams{RefundID: id, ApprovedAmount: req.AmountCents, ApprovedBy: pgtype.Int8{Int64: uid, Valid: true}, ApprovalReason: pgtype.Text{String: req.Reason, Valid: true}, EntitlementAction: req.EntitlementAction, EntitlementEffectiveAt: effective, TargetTermOrderID: target})
	if err != nil {
		mapRefundApprovalError(c, err)
		return
	}
	c.JSON(200, gin.H{"refund": row, "execution_status": "awaiting_worker"})
}
func mapRefundApprovalError(c *gin.Context, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 409, "REFUND_STATE_CONFLICT")
		return
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Message {
		case "REFUND_APPROVAL_REQUIRED", "REFUND_ENTITLEMENT_TARGET_INVALID", "REFUND_FULL_PERIOD_REQUIRED", "REFUND_EXCEEDS_PAYMENT", "REFUND_ADJUSTMENTS_OUTSTANDING", "REFUND_FUTURE_TERM_REQUIRED", "REFUND_CURRENT_TERM_REQUIRED":
			authError(c, 409, pg.Message)
			return
		}
	}
	authError(c, 500, "DB_ERROR")
}
