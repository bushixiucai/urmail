package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

// QuotaStore reads the counters needed to enforce resource quotas on create endpoints.
type QuotaStore interface {
	CountDomainsByTenant(ctx context.Context, tenantID int64) (int64, error)
	CountMailboxesByTenant(ctx context.Context, tenantID int64) (int64, error)
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

// resolveMailboxResourceLimit reads a quota limit from settings following precedence:
// Tenant scope override > Platform scope base default > fallback (-1 = unlimited).
func resolveMailboxResourceLimit(ctx context.Context, q QuotaStore, tenantID int64, key string) int64 {
	reader, ok := q.(interface {
		GetTenantSetting(context.Context, store.GetTenantSettingParams) (store.Settings, error)
		PlatformGetSetting(context.Context, string) (store.PlatformGetSettingRow, error)
	})
	if !ok {
		return -1
	}
	// 1. Tenant-level override takes precedence ("tenant_quota 优先")
	if row, err := reader.GetTenantSetting(ctx, store.GetTenantSettingParams{ScopeID: tenantID, Key: key}); err == nil && len(row.Value) > 0 {
		trimmed := bytes.TrimSpace(row.Value)
		if !bytes.Equal(trimmed, []byte("null")) {
			var n int64
			if json.Unmarshal(trimmed, &n) == nil {
				return n
			}
		}
	}
	// 2. Platform-level default
	if row, err := reader.PlatformGetSetting(ctx, key); err == nil && len(row.Value) > 0 {
		trimmed := bytes.TrimSpace(row.Value)
		if !bytes.Equal(trimmed, []byte("null")) {
			var n int64
			if json.Unmarshal(trimmed, &n) == nil {
				return n
			}
		}
	}
	return -1
}

// quotaCheck verifies that creating one more resource of kind stays within
// the tenant's current limits configured in settings.
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
	var limit int64
	var exceededCode string
	var err error

	switch kind {
	case "domain":
		used, err = q.CountDomainsByTenant(c, tenantID)
		limit = resolveMailboxResourceLimit(c, q, tenantID, "mailbox_domain_limit")
		exceededCode = "DOMAIN_QUOTA_EXCEEDED"
	case "mailbox":
		used, err = q.CountMailboxesByTenant(c, tenantID)
		limit = resolveMailboxResourceLimit(c, q, tenantID, "mailbox_limit")
		exceededCode = "MAILBOX_LIMIT_EXCEEDED"
	default:
		return true
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return false
	}

	if limit >= 0 && used >= limit {
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
