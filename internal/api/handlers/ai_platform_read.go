package handlers

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func (h *AIConfigurationHandler) platformTenant(c *gin.Context) (int64, bool) {
	if h.Pool == nil {
		authError(c, 503, "AI_DISABLED")
		return 0, false
	}
	uid, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return 0, false
	}
	allowed, err := store.New(h.Pool).CheckPlatformAdmin(c, uid)
	if err != nil || !allowed {
		authError(c, 403, "FORBIDDEN")
		return 0, false
	}
	tenant, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || tenant <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return 0, false
	}
	return tenant, true
}

// PlatformGet returns configuration and tenant-consent metadata, never credentials.
func (h *AIConfigurationHandler) PlatformGet(c *gin.Context) {
	tenant, ok := h.platformTenant(c)
	if !ok {
		return
	}
	q := store.New(h.Pool)
	cfg, err := q.PlatformGetAIProvider(c, tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(200, gin.H{"configuration": nil, "configured": false, "cloud_endpoints": safelyListedEndpoints(h.CloudEndpoints), "tenant_consent": gin.H{"allowed": false, "tenant_local_only": true, "requires_confirmation": true, "scope_hash": AICloudScopeHash}})
		return
	}
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	policy, err := aiTargetPolicy(c, q, cfg)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"configured": true, "configuration": gin.H{"mode": cfg.Mode, "provider": cfg.Provider, "endpoint": cfg.Endpoint, "model": cfg.Model, "enabled": cfg.Enabled, "credential_configured": cfg.EncryptedKey != "", "input_micros_per_million": cfg.InputMicrosPerMillion, "output_micros_per_million": cfg.OutputMicrosPerMillion, "cost_currency": cfg.CostCurrency}, "tenant_consent": policy, "cloud_endpoints": safelyListedEndpoints(h.CloudEndpoints)})
}

// PlatformUsage prioritizes unfinished accounting metadata without model input/output.
func (h *AIConfigurationHandler) PlatformUsage(c *gin.Context) {
	tenant, ok := h.platformTenant(c)
	if !ok {
		return
	}
	rows, err := store.New(h.Pool).PlatformPendingAIUsage(c, tenant)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	metadata := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		item := aiTokenMetadata(row.ReservedTokens, row.ActualTokens)
		item["id"] = row.ID
		item["mailbox_id"] = row.MailboxID
		item["mode"] = row.Mode
		item["provider"] = row.Provider
		item["model"] = row.Model
		item["cost_currency"] = row.CostCurrency
		item["status"] = row.Status
		item["reserved_cost_micros"] = row.ReservedCostMicros
		item["actual_cost_micros"] = row.ActualCostMicros
		item["period_start"] = row.PeriodStart
		item["period_end"] = row.PeriodEnd
		item["updated_at"] = row.UpdatedAt
		metadata = append(metadata, item)
	}
	c.JSON(200, gin.H{"requests": metadata})
}

// PlatformHealth applies the same tenant export policy to metadata diagnostics.
func (h *AIConfigurationHandler) PlatformHealth(c *gin.Context) {
	tenant, ok := h.platformTenant(c)
	if !ok {
		return
	}
	c.Set(middleware.ContextTenantID, tenant)
	h.Health(c)
}
