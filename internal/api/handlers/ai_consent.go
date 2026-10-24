package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// AICloudScopeHash pins the exact content and suggestion-only operation contract.
const AICloudScopeHash = "43a8dfa67f89e1fa6861bf0f3c283bce46f112511c472b36e3174e30d1b037fe"

var errAIConsent = errors.New("tenant cloud export consent required")
var errAITargetChanged = errors.New("AI target configuration changed")

func aiTargetPolicy(ctx context.Context, q *store.Queries, cfg store.AiProviderConfigs) (gin.H, error) {
	allowed := cfg.Enabled && cfg.Provider == "ollama" && cfg.Mode == "local"
	if cfg.Provider != "ollama" {
		var err error
		allowed, err = q.AICloudAuthorized(ctx, store.AICloudAuthorizedParams{TenantID: cfg.TenantID, ConfigurationDigest: cfg.ConfigurationDigest, ScopeHash: AICloudScopeHash})
		if err != nil {
			return nil, err
		}
	}
	return gin.H{"allowed": allowed, "provider": cfg.Provider, "model": cfg.Model, "endpoint": cfg.Endpoint, "configuration_digest": cfg.ConfigurationDigest, "scope_hash": AICloudScopeHash, "requires_confirmation": cfg.Provider != "ollama", "tenant_local_only": cfg.TenantLocalOnly, "scope_key": "page.ai.cloud_scope"}, nil
}

// Consent permits only a tenant owner/admin to authorize the exact current target.
// Denial/local-only is always permitted even when a previously viewed target changed.
func (h *AIConfigurationHandler) Consent(c *gin.Context) {
	role := middleware.GetRole(c)
	if role != "owner" && role != "admin" {
		authError(c, 403, "FORBIDDEN")
		return
	}
	var input struct {
		ConfigurationDigest string `json:"configuration_digest"`
		ScopeHash           string `json:"scope_hash"`
		AllowCloud          bool   `json:"allow_cloud"`
		Confirmed           bool   `json:"confirmed"`
	}
	if c.ShouldBindJSON(&input) != nil || input.AllowCloud && (!input.Confirmed || len(input.ConfigurationDigest) != 64 || input.ScopeHash != AICloudScopeHash) {
		authError(c, 400, "AI_CLOUD_CONSENT_REQUIRED")
		return
	}
	raw, ok := middleware.QueriesFromCtx(c)
	q, scoped := raw.(*store.Queries)
	if !ok || !scoped {
		authError(c, 503, "DB_ERROR")
		return
	}
	if err := q.TenantConsentAICloud(c, store.TenantConsentAICloudParams{ConfigurationDigest: input.ConfigurationDigest, ScopeHash: input.ScopeHash, AllowCloud: input.AllowCloud}); err != nil {
		authError(c, 409, "AI_CLOUD_CONSENT_STALE")
		return
	}
	c.Status(http.StatusNoContent)
}
