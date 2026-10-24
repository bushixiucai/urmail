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

// List returns the current tenant's audit records. Time range, actor, action
// and keyset (before_id) filters are optional; id is the stable pagination
// tiebreaker so a page never shifts while rows are appended (M6 review D11).
func (h *AuditHandler) List(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	params := store.ListAuditLogsByTenantParams{
		TenantID: pgtype.Int8{Int64: tenantID, Valid: true},
		Limit:    100,
	}
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 32); err == nil && v > 0 && v <= 500 {
			params.Limit = int32(v)
		}
	}
	if raw := c.Query("actor_user_id"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v > 0 {
			params.ActorUserID = pgtype.Int8{Int64: v, Valid: true}
		}
	}
	if raw := c.Query("action"); raw != "" {
		params.Action = pgtype.Text{String: raw, Valid: true}
	}
	if raw := c.Query("from"); raw != "" {
		if t, err := parseTimeParam(raw); err == nil {
			params.FromTime = pgtype.Timestamptz{Time: t, Valid: true}
		}
	}
	if raw := c.Query("to"); raw != "" {
		if t, err := parseTimeParam(raw); err == nil {
			params.ToTime = pgtype.Timestamptz{Time: t, Valid: true}
		}
	}
	if raw := c.Query("before_id"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v > 0 {
			params.BeforeID = pgtype.Int8{Int64: v, Valid: true}
		}
	}
	logs, err := requestStore(c, h.Store).ListAuditLogsByTenant(c, params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"logs": logs})
}
