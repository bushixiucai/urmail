package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/store"
)

// QuotaStore reads the counters and plan limits needed to enforce
// subscription quotas on create endpoints.
type QuotaStore interface {
	CountDomainsByTenant(ctx context.Context, tenantID int64) (int64, error)
	CountMailboxesByTenant(ctx context.Context, tenantID int64) (int64, error)
	GetActiveSubscriptionByTenant(ctx context.Context, tenantID int64) (store.Subscriptions, error)
	GetPlanByCode(ctx context.Context, code string) (store.Plans, error)
}

// quotaCheck verifies that creating one more resource of kind stays within
// the tenant's plan. When the tenant has no active subscription no plan
// limits exist yet and creation is allowed; plan enforcement starts with
// the first subscription (M5 wires the default free plan).
func quotaCheck(c *gin.Context, q QuotaStore, tenantID int64, kind string) bool {
	if q == nil {
		// Unit tests without a database: no enforcement possible.
		return true
	}
	var used int64
	var limit int32
	var exceededCode string

	sub, err := q.GetActiveSubscriptionByTenant(c, tenantID)
	if err != nil {
		// No active subscription: no plan to enforce.
		return true
	}
	plan, err := q.GetPlanByCode(c, sub.PlanID)
	if err != nil {
		return true
	}

	switch kind {
	case "domain":
		used, err = q.CountDomainsByTenant(c, tenantID)
		limit = plan.DomainLimit
		exceededCode = "DOMAIN_QUOTA_EXCEEDED"
	case "mailbox":
		used, err = q.CountMailboxesByTenant(c, tenantID)
		limit = plan.MailboxLimit
		exceededCode = "MAILBOX_QUOTA_EXCEEDED"
	default:
		return true
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return false
	}

	if limit >= 0 && used >= int64(limit) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"code":    exceededCode,
				"message": "error." + exceededCode,
			},
		})
		return false
	}
	return true
}
