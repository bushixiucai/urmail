package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainSettings "github.com/bushixiucai/urmail/internal/domain/settings"
	"github.com/bushixiucai/urmail/internal/store"
)

type TenantStore interface {
	GetTenantByID(ctx context.Context, id int64) (store.Tenants, error)
	ListSettingsByScopes(ctx context.Context, arg store.ListSettingsByScopesParams) ([]store.Settings, error)
	CountDomainsByTenant(ctx context.Context, tenantID int64) (int64, error)
	CountMailboxesByTenant(ctx context.Context, tenantID int64) (int64, error)
	GetActiveSubscriptionByTenant(ctx context.Context, tenantID int64) (store.Subscriptions, error)
	GetPlanByCode(ctx context.Context, code string) (store.Plans, error)
}

type TenantHandler struct {
	Store TenantStore
}

func NewTenantHandler(s TenantStore) *TenantHandler {
	return &TenantHandler{Store: s}
}

func (h *TenantHandler) Current(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "NO_TENANT_CONTEXT", "message": "auth.no_tenant"},
		})
		return
	}

	tenant, err := q.GetTenantByID(c, tenantID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{"code": "TENANT_NOT_FOUND", "message": "tenant.not_found"},
		})
		return
	}

	// 1. Fetch 3-layer settings: Platform (0), Brand (tenant.BrandID), Tenant (tenant.ID)
	rawSettings, _ := q.ListSettingsByScopes(c, store.ListSettingsByScopesParams{
		ScopeID:   tenant.BrandID,
		ScopeID_2: tenant.ID,
	})

	entries := make([]domainSettings.SettingEntry, 0, len(rawSettings))
	for _, s := range rawSettings {
		entries = append(entries, domainSettings.SettingEntry{
			Scope:   domainSettings.Scope(s.Scope),
			ScopeID: s.ScopeID,
			Key:     s.Key,
			Value:   json.RawMessage(s.Value),
		})
	}
	mergedSettings, _ := domainSettings.Merge3Layers(entries)

	// 2. Fetch resource usage and subscription
	domainCount, _ := q.CountDomainsByTenant(c, tenantID)
	mailboxCount, _ := q.CountMailboxesByTenant(c, tenantID)
	sub, subErr := q.GetActiveSubscriptionByTenant(c, tenantID)

	var plan store.Plans
	if subErr == nil {
		plan, _ = q.GetPlanByCode(c, sub.PlanID)
	}

	c.JSON(http.StatusOK, gin.H{
		"tenant": gin.H{
			"id":         tenant.ID,
			"name":       tenant.Name,
			"slug":       tenant.Slug,
			"status":     tenant.Status,
			"created_at": tenant.CreatedAt,
		},
		"settings": mergedSettings,
		"usage": gin.H{
			"domains_used":   domainCount,
			"domains_limit":  plan.DomainLimit,
			"mailboxes_used": mailboxCount,
			"mailboxes_limit": plan.MailboxLimit,
			"storage_bytes_limit": plan.QuotaBytes,
		},
		"subscription": gin.H{
			"plan_code": plan.Code,
			"plan_name": plan.Name,
			"status":    sub.Status,
		},
	})
}