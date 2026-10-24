package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/domain/billing"
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

// mailboxUnderLegalHold reports whether any live archive entry of the
// mailbox carries a legal hold.
func mailboxUnderLegalHold(c *gin.Context, q interface {
	ListMailboxesWithLegalHold(ctx context.Context, tenantID int64) ([]pgtype.Int8, error)
}, tenantID, mailboxID int64) (bool, error) {
	holds, err := q.ListMailboxesWithLegalHold(c, tenantID)
	if err != nil {
		return false, err
	}
	for _, h := range holds {
		if h.Valid && h.Int64 == mailboxID {
			return true, nil
		}
	}
	return false, nil
}

// quotaCheck verifies that creating one more resource of kind stays within
// the tenant's current effective plan. Missing and expired subscriptions fail
// closed; restricted onboarding may separately allow a pending domain.
func quotaCheck(c *gin.Context, q QuotaStore, tenantID int64, kind string) bool {
	if q == nil {
		// Unit tests without a database: no enforcement possible.
		return true
	}
	if kind == "domain" {
		if pending, ok := q.(interface {
			CanCreatePendingDomain(context.Context, int64) (bool, error)
		}); ok {
			allowed, err := pending.CanCreatePendingDomain(c, tenantID)
			if err != nil {
				authError(c, 500, "DB_ERROR")
				return false
			}
			if allowed {
				return true
			}
		}
	}
	var used int64
	var limit int32
	var exceededCode string

	sub, err := q.GetActiveSubscriptionByTenant(c, tenantID)
	if err != nil {
		code := "DB_ERROR"
		if errors.Is(err, pgx.ErrNoRows) {
			code = "SUBSCRIPTION_REQUIRED"
		}
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": code}})
		return false
	}
	now := time.Now()
	if !serviceEntitlement(sub).CurrentAt(now) {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "SUBSCRIPTION_EXPIRED"}})
		return false
	}
	if queries, ok := q.(*store.Queries); ok {
		allowed, lookupErr := queries.CurrentPaidEntitlementAllowed(c, tenantID)
		if lookupErr != nil {
			authError(c, 500, "DB_ERROR")
			return false
		}
		if !allowed.Valid || !allowed.Bool {
			authError(c, 409, "SUBSCRIPTION_EXPIRED")
			return false
		}
	}
	plan, err := subscriptionPlan(c, q, sub)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return false
	}

	switch kind {
	case "domain":
		used, err = q.CountDomainsByTenant(c, tenantID)
		limit = plan.DomainLimit
		exceededCode = "DOMAIN_QUOTA_EXCEEDED"
	case "mailbox":
		used, err = q.CountMailboxesByTenant(c, tenantID)
		limit = plan.MailboxLimit
		exceededCode = "MAILBOX_LIMIT_EXCEEDED"
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

// subscriptionPlan reads the frozen current entitlement rather than an editable catalog row.
func subscriptionPlan(ctx context.Context, q interface {
	GetPlanByCode(context.Context, string) (store.Plans, error)
}, sub store.Subscriptions) (store.Plans, error) {
	if effective, ok := q.(interface {
		GetEffectivePlan(context.Context, int64) (store.Plans, error)
	}); ok {
		return effective.GetEffectivePlan(ctx, sub.TenantID)
	}
	if len(sub.PlanSnapshot) > 0 {
		var frozen PlanResponse
		if err := json.Unmarshal(sub.PlanSnapshot, &frozen); err != nil {
			return store.Plans{}, err
		}
		return store.Plans{Code: frozen.Code, Name: frozen.Name, Description: frozen.Description, PriceCents: frozen.PriceCents, Currency: frozen.Currency, QuotaBytes: frozen.QuotaBytes, MailboxLimit: frozen.MailboxLimit, DomainLimit: frozen.DomainLimit, DailySendLimit: frozen.DailySendLimit, Features: frozen.Features, SortOrder: frozen.SortOrder, Active: frozen.Active}, nil
	}
	return q.GetPlanByCode(ctx, sub.PlanID)
}

// serviceEntitlement preserves SQL NULL without accepting PostgreSQL infinity as an end.
func serviceEntitlement(sub store.Subscriptions) billing.ServiceEntitlement {
	e := billing.ServiceEntitlement{Source: sub.EntitlementSource, Status: sub.Status}
	if sub.MigrationManifestSha256.Valid {
		e.ManifestSHA256 = sub.MigrationManifestSha256.String
	}
	if sub.PeriodStart.Valid && sub.PeriodStart.InfinityModifier == pgtype.Finite {
		e.PeriodStart = sub.PeriodStart.Time
	}
	if sub.PeriodEnd.Valid {
		end := time.Time{}
		if sub.PeriodEnd.InfinityModifier == pgtype.Finite {
			end = sub.PeriodEnd.Time
		}
		e.PeriodEnd = &end
	}
	if sub.TrialEndsAt.Valid {
		end := time.Time{}
		if sub.TrialEndsAt.InfinityModifier == pgtype.Finite {
			end = sub.TrialEndsAt.Time
		}
		e.TrialEndsAt = &end
	}
	return e
}
