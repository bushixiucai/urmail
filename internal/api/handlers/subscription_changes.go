package handlers

import (
	"errors"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

// ScheduleChange records the intended next paid plan, without collecting funds
// or granting service beyond the last already paid period.
func (h *BillingHandler) ScheduleChange(c *gin.Context) {
	tid, ok := middleware.GetTenantID(c)
	uid, userOK := middleware.GetUserID(c)
	if !ok || !userOK {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	var body struct {
		PlanCode string `json:"plan_code" binding:"required"`
	}
	if c.ShouldBindJSON(&body) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	q, ok := middleware.QueriesFromCtx(c)
	if !ok {
		authError(c, 503, "DB_ERROR")
		return
	}
	queries, ok := q.(*store.Queries)
	if !ok {
		authError(c, 503, "DB_ERROR")
		return
	}
	if err := queries.PlatformLockBillingTenant(c, tid); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	sub, err := queries.GetActiveSubscriptionByTenant(c, tid)
	if err != nil {
		authError(c, 409, "SUBSCRIPTION_REQUIRED")
		return
	}
	if sub.EntitlementSource == billing.EntitlementMigration {
		authError(c, 409, "MIGRATION_ENTITLEMENT_PRESERVED")
		return
	}
	change, err := queries.ScheduleSubscriptionChange(c, store.ScheduleSubscriptionChangeParams{TenantID: tid, TargetPlanCode: body.PlanCode, EffectiveAt: sub.PeriodEnd, RequestedBy: uid})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23514" {
			switch pg.Message {
			case "SUBSCRIPTION_CHANGED", "PLAN_NOT_FOUND", "PLAN_CAPACITY_EXCEEDED":
				authError(c, 409, pg.Message)
			default:
				authError(c, 409, "PLAN_CHANGE_REVIEW_REQUIRED")
			}
			return
		}
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"change": change, "payment_required": true})
}

// CancelSubscription toggles auto-renew for the paid subscription. The
// explicit desired state comes from the request body (`cancel_at_period_end`);
// an empty body keeps the historical default of turning auto-renew off at the
// paid period end. Re-enabling is the same authorized endpoint with
// `cancel_at_period_end:false`. The prepaid period and all stored terms are
// preserved either way.
func (h *BillingHandler) CancelSubscription(c *gin.Context) {
	tid, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	var body struct {
		CancelAtPeriodEnd *bool `json:"cancel_at_period_end"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	desired := body.CancelAtPeriodEnd == nil || *body.CancelAtPeriodEnd
	q, ok := middleware.QueriesFromCtx(c)
	if !ok {
		authError(c, 503, "DB_ERROR")
		return
	}
	queries, ok := q.(*store.Queries)
	if !ok {
		authError(c, 503, "DB_ERROR")
		return
	}
	if err := queries.PlatformLockBillingTenant(c, tid); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	current, lookupErr := queries.GetActiveSubscriptionByTenant(c, tid)
	if lookupErr != nil {
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			authError(c, 404, "SUBSCRIPTION_REQUIRED")
		} else {
			authError(c, 500, "DB_ERROR")
		}
		return
	}
	if current.EntitlementSource == billing.EntitlementMigration {
		authError(c, 409, "MIGRATION_ENTITLEMENT_PRESERVED")
		return
	}
	sub, err := queries.CancelSubscriptionAtPaidEnd(c, store.CancelSubscriptionAtPaidEndParams{TenantID: tid, CancelAtPeriodEnd: desired})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 404, "SUBSCRIPTION_REQUIRED")
		return
	}
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"subscription": sub, "effective_at": sub.PeriodEnd})
}

// CancelScheduledChange cancels only an unfunded intention; paid terms require refund approval.
func (h *BillingHandler) CancelScheduledChange(c *gin.Context) {
	tid, tenantOK := middleware.GetTenantID(c)
	uid, userOK := middleware.GetUserID(c)
	if !tenantOK || !userOK {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	q, ok := middleware.QueriesFromCtx(c)
	if !ok {
		authError(c, 503, "DB_ERROR")
		return
	}
	queries, ok := q.(*store.Queries)
	if !ok {
		authError(c, 503, "DB_ERROR")
		return
	}
	if err := queries.PlatformLockBillingTenant(c, tid); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	change, err := queries.CancelScheduledSubscriptionChange(c, store.CancelScheduledSubscriptionChangeParams{TenantID: tid, RequestedBy: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 404, "PLAN_CHANGE_NOT_FOUND")
		return
	}
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23514" {
			authError(c, 409, "PLAN_CHANGE_ALREADY_PAID")
			return
		}
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"change": change})
}
