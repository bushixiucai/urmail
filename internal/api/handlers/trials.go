package handlers

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// RedeemTrial grants a bounded trial through one serialized database routine.
func (h *BillingHandler) RedeemTrial(c *gin.Context) {
	tid, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	var req struct {
		DomainID   int64  `json:"domain_id" binding:"required"`
		CouponCode string `json:"coupon_code" binding:"required"`
		PlanCode   string `json:"plan_code" binding:"required"`
	}
	if c.ShouldBindJSON(&req) != nil || req.DomainID < 1 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	raw, ok := middleware.QueriesFromCtx(c)
	if !ok {
		authError(c, 503, "DB_ERROR")
		return
	}
	q, ok := raw.(*store.Queries)
	if !ok {
		authError(c, 503, "DB_ERROR")
		return
	}
	end, err := q.RedeemTenantTrial(c, store.RedeemTenantTrialParams{TenantID: tid, DomainID: req.DomainID, CouponCode: req.CouponCode, PlanCode: req.PlanCode})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			switch pg.Message {
			case "MIGRATION_ENTITLEMENT_EXISTS", "TRIAL_ALREADY_USED", "DOMAIN_VERIFICATION_REQUIRED", "PAID_ENTITLEMENT_EXISTS", "COUPON_INVALID", "TRIAL_PLAN_UNAVAILABLE":
				authError(c, 409, pg.Message)
				return
			}
			if pg.Code == "CU002" {
				authError(c, 409, "COUPON_EXHAUSTED")
				return
			}
		}
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(201, gin.H{"status": "trialing", "ends_at": end, "onboarding_status": "pending"})
}
