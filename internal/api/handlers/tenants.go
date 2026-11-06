package handlers

import (
	"context"
	"encoding/json"
	"math"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainSettings "github.com/bushixiucai/urmail/internal/domain/settings"
	"github.com/bushixiucai/urmail/internal/store"
)

// TenantStore defines the tenant management contract.
type TenantStore interface {
	GetTenantByID(ctx context.Context, id int64) (store.Tenants, error)
	ListSettingsByScopes(ctx context.Context, arg store.ListSettingsByScopesParams) ([]store.Settings, error)
	CountDomainsByTenant(ctx context.Context, tenantID int64) (int64, error)
	CountMailboxesByTenant(ctx context.Context, tenantID int64) (int64, error)
}

// TenantPlatformStore reads platform-scoped rows needed by the tenant view.
// The brand row must bypass RLS because a tenant reads its brand's theme.
type TenantPlatformStore interface {
	PlatformGetBrandThemeByTenant(ctx context.Context, tenantID int64) (store.PlatformGetBrandThemeByTenantRow, error)
}

// TenantHandler defines the tenant management contract.
type TenantHandler struct {
	Store    TenantStore
	Platform TenantPlatformStore
}

// NewTenantHandler serves the tenant management operation.
func NewTenantHandler(s TenantStore, p TenantPlatformStore) *TenantHandler {
	return &TenantHandler{Store: s, Platform: p}
}

// TenantResponse is the explicit public shape of the current tenant. Only
// fields the UI needs are exposed; brand/owner columns and raw settings JSON
// from the store row stay server-side.
type TenantResponse struct {
	ID        int64              `json:"id"`
	Name      string             `json:"name"`
	Slug      string             `json:"slug"`
	Status    string             `json:"status"`
	CreatedAt pgtype.Timestamptz `json:"created_at"`
}

// TenantUsageResponse reports plan consumption for the current tenant.
type TenantUsageResponse struct {
	DomainsUsed           int64   `json:"domains_used"`
	DomainsLimit          int32   `json:"domains_limit"`
	MailboxesUsed         int64   `json:"mailboxes_used"`
	MailboxesLimit        int32   `json:"mailboxes_limit"`
	StorageBytesLimit     int64   `json:"storage_bytes_limit"`
	StorageBytesUsed      *int64  `json:"storage_bytes_used"`
	StorageBytesAllocated int64   `json:"storage_bytes_allocated"`
	StorageMeasuredAt     any     `json:"storage_measured_at"`
	StorageError          *string `json:"storage_error"`
	StorageStatus         string  `json:"storage_status"`
}

// TenantSubscriptionResponse is the plan summary shown on the dashboard.
type TenantSubscriptionResponse struct {
	PlanCode string `json:"plan_code"`
	// PlanName is the plan's i18n JSON object (for example
	// {"zh-CN":"标准版","en":"Standard"}); it is embedded as-is.
	PlanName json.RawMessage `json:"plan_name"`
	Status   string          `json:"status"`
}

// TenantCurrentResponse is the complete body of GET /api/v1/tenants/current.
type TenantCurrentResponse struct {
	Tenant       TenantResponse                 `json:"tenant"`
	Settings     map[string]json.RawMessage     `json:"settings"`
	Theme        *domainSettings.EffectiveTheme `json:"theme"`
	Usage        TenantUsageResponse            `json:"usage"`
	Subscription TenantSubscriptionResponse     `json:"subscription"`
}

// Current serves the tenant management operation.
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
	for key := range mergedSettings {
		if !publicSettingKey(key) {
			delete(mergedSettings, key)
		}
	}

	// Effective brand theme: the brand row (platform-scoped) deep-merged
	// with the platform/brand/tenant settings entries keyed "theme".
	var theme *domainSettings.EffectiveTheme
	if h.Platform != nil {
		if brandRow, err := h.Platform.PlatformGetBrandThemeByTenant(c, tenantID); err == nil {
			src := domainSettings.BrandThemeSource{
				BrandID:      brandRow.BrandID,
				BrandSlug:    brandRow.BrandSlug,
				Theme:        json.RawMessage(brandRow.Theme),
				LogoURL:      textPtr(brandRow.LogoUrl),
				FaviconURL:   textPtr(brandRow.FaviconUrl),
				PrimaryColor: textPtr(brandRow.PrimaryColor),
			}
			theme, _ = domainSettings.BuildEffectiveTheme(src, entries)
		}
	}

	// 2. Fetch resource usage. Limits come only from settings, never plans.
	domainCount, _ := q.CountDomainsByTenant(c, tenantID)
	mailboxCount, _ := q.CountMailboxesByTenant(c, tenantID)

	domainLimit := int32(-1)
	mailboxLimit := int32(-1)
	quotaBytes := int64(-1)
	if v, ok := mergedSettings["mailbox_domain_limit"]; ok {
		var n int64
		if json.Unmarshal(v, &n) == nil && n >= -1 && n <= math.MaxInt32 {
			domainLimit = int32(n)
		}
	}
	if v, ok := mergedSettings["mailbox_limit"]; ok {
		var n int64
		if json.Unmarshal(v, &n) == nil && n >= -1 && n <= math.MaxInt32 {
			mailboxLimit = int32(n)
		}
	}
	if v, ok := mergedSettings["mailbox_quota_bytes"]; ok {
		var n int64
		if json.Unmarshal(v, &n) == nil && n >= -1 {
			quotaBytes = n
		}
	}

	usage := TenantUsageResponse{DomainsUsed: domainCount, DomainsLimit: domainLimit, MailboxesUsed: mailboxCount, MailboxesLimit: mailboxLimit, StorageBytesLimit: quotaBytes, StorageStatus: "unknown"}
	if usageStore := requestStore[interface {
		GetTenantStorageUsage(context.Context, int64) (store.GetTenantStorageUsageRow, error)
	}](c, nil); usageStore != nil {
		row, err := usageStore.GetTenantStorageUsage(c, tenantID)
		if err == nil {
			usage.StorageBytesAllocated = row.BytesAllocated
			usage.StorageMeasuredAt = row.MeasuredAt
			if row.UnknownCount == 0 {
				usage.StorageBytesUsed = &row.BytesUsed
				usage.StorageStatus = "measured"
			} else {
				code := "STORAGE_USAGE_UNKNOWN"
				usage.StorageError = &code
			}
			if row.Unlimited {
				usage.StorageBytesAllocated = -1
			}
		} else {
			code := "DB_ERROR"
			usage.StorageError = &code
		}
	}

	c.JSON(http.StatusOK, TenantCurrentResponse{
		Tenant: TenantResponse{
			ID:        tenant.ID,
			Name:      tenant.Name,
			Slug:      tenant.Slug,
			Status:    tenant.Status,
			CreatedAt: tenant.CreatedAt,
		},
		Settings: mergedSettings,
		Theme:    theme,
		Usage:    usage,
		Subscription: TenantSubscriptionResponse{
			PlanName: json.RawMessage(`{}`),
		},
	})
}

// textPtr converts a nullable text column into an optional string.
func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	value := t.String
	return &value
}
