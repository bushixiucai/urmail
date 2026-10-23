package handlers

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// AuditStore reads append-only audit records in the tenant transaction.
type AuditStore interface {
	ListAuditLogsByTenant(ctx context.Context, arg store.ListAuditLogsByTenantParams) ([]store.AuditLogs, error)
}

// AuditHandler serves tenant audit history.
type AuditHandler struct {
	Store AuditStore
}

// NewAuditHandler binds the audit query dependency.
func NewAuditHandler(s AuditStore) *AuditHandler {
	return &AuditHandler{Store: s}
}

// List returns the current tenant's most recent audit records.
func (h *AuditHandler) List(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	limit := int32(100)
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 32); err == nil && v > 0 && v <= 500 {
			limit = int32(v)
		}
	}
	logs, err := requestStore(c, h.Store).ListAuditLogsByTenant(c, store.ListAuditLogsByTenantParams{
		TenantID: pgtype.Int8{Int64: tenantID, Valid: true},
		Limit:    limit,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"logs": logs})
}
