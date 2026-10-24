package handlers

import (
	"context"
	"net/http"
	"net/mail"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/db"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

// MarketingSuppressionStore provides tenant-scoped suppression CRUD.
type MarketingSuppressionStore interface {
	AddMarketingSuppression(context.Context, store.AddMarketingSuppressionParams) (store.MarketingSuppressions, error)
	ListMarketingSuppressions(context.Context, int64) ([]store.MarketingSuppressions, error)
	RemoveMarketingSuppression(context.Context, store.RemoveMarketingSuppressionParams) (int64, error)
}

// UnsubscribeTokenVerifier validates a stateless, signed unsubscribe token.
type UnsubscribeTokenVerifier interface {
	VerifyUnsubscribeToken(string) (int64, string, bool)
}

// MarketingSuppressionHandler serves public unsubscribe and tenant admin operations.
type MarketingSuppressionHandler struct {
	Store   MarketingSuppressionStore
	Tokens  UnsubscribeTokenVerifier
	AppPool *db.Pool
}

func normalizeSuppressionEmail(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	addr, err := mail.ParseAddress(raw)
	if err != nil || addr.Address != raw {
		return "", false
	}
	parts := strings.Split(addr.Address, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return strings.ToLower(addr.Address), true
}

type marketingSuppressionRequest struct {
	Email string `json:"email"`
}

// OneClick is public by design: a valid unexpired signature is its authorization.
func (h *MarketingSuppressionHandler) OneClick(c *gin.Context) {
	if h.Tokens == nil || h.AppPool == nil || h.Store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "UNSUBSCRIBE_UNAVAILABLE"}})
		return
	}
	token := c.Query("token")
	if token == "" {
		var input struct {
			Token string `json:"token"`
		}
		if c.ShouldBindJSON(&input) == nil {
			token = input.Token
		}
	}
	tenantID, email, ok := h.Tokens.VerifyUnsubscribeToken(token)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_UNSUBSCRIBE_TOKEN"}})
		return
	}
	tx, err := h.AppPool.TenantTx(c.Request.Context(), tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "UNSUBSCRIBE_FAILED"}})
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = store.New(tx).AddMarketingSuppression(c.Request.Context(), store.AddMarketingSuppressionParams{TenantID: tenantID, Email: email, Reason: domainMail.SuppressionReasonUnsubscribe})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "UNSUBSCRIBE_FAILED"}})
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "UNSUBSCRIBE_FAILED"}})
		return
	}
	c.Status(http.StatusNoContent)
}

// List returns the current tenant's durable suppression entries.
func (h *MarketingSuppressionHandler) List(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "NO_TENANT_CONTEXT"}})
		return
	}
	items, err := requestStore(c, h.Store).ListMarketingSuppressions(c.Request.Context(), tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"suppressions": items})
}

// Add creates or refreshes a manual suppression for the active tenant.
func (h *MarketingSuppressionHandler) Add(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "NO_TENANT_CONTEXT"}})
		return
	}
	var input marketingSuppressionRequest
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_REQUEST"}})
		return
	}
	email, valid := normalizeSuppressionEmail(input.Email)
	if !valid {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_EMAIL"}})
		return
	}
	item, err := requestStore(c, h.Store).AddMarketingSuppression(c.Request.Context(), store.AddMarketingSuppressionParams{TenantID: tenantID, Email: email, Reason: domainMail.SuppressionReasonManual})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"suppression": item})
}

// Remove deletes a manual suppression from the active tenant.
func (h *MarketingSuppressionHandler) Remove(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "NO_TENANT_CONTEXT"}})
		return
	}
	var input marketingSuppressionRequest
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_REQUEST"}})
		return
	}
	email, valid := normalizeSuppressionEmail(input.Email)
	if !valid {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_EMAIL"}})
		return
	}
	_, err := requestStore(c, h.Store).RemoveMarketingSuppression(c.Request.Context(), store.RemoveMarketingSuppressionParams{TenantID: tenantID, Email: email})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.Status(http.StatusNoContent)
}
