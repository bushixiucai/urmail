package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// ComplianceGrantStore preserves owner-issued content authorization records.
type ComplianceGrantStore interface {
	ListComplianceGrantsByTenant(context.Context, int64) ([]store.ComplianceGrants, error)
	ListComplianceGrantRecipients(context.Context, int64) ([]store.ListComplianceGrantRecipientsRow, error)
	CreateComplianceGrant(context.Context, store.CreateComplianceGrantParams) (store.ComplianceGrants, error)
	RevokeComplianceGrant(context.Context, store.RevokeComplianceGrantParams) (store.ComplianceGrants, error)
}

// ComplianceGrantHandler manages explicit owner authorizations, never IMAP credentials.
type ComplianceGrantHandler struct{ Store ComplianceGrantStore }

func requireContentOwner(c *gin.Context) bool {
	if middleware.GetRole(c) != "owner" {
		authError(c, http.StatusForbidden, "FORBIDDEN_ROLE")
		return false
	}
	return true
}

// List includes historical grants so owners can review unconfirmed records.
func (h *ComplianceGrantHandler) List(c *gin.Context) {
	if !requireContentOwner(c) {
		return
	}
	tid, _ := middleware.GetTenantID(c)
	q := requestStore(c, h.Store)
	grants, err := q.ListComplianceGrantsByTenant(c, tid)
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	recipients, err := q.ListComplianceGrantRecipients(c, tid)
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if grants == nil {
		grants = []store.ComplianceGrants{}
	}
	if recipients == nil {
		recipients = []store.ListComplianceGrantRecipientsRow{}
	}
	c.JSON(200, gin.H{"grants": grants, "recipients": recipients})
}

// Create requires a reason and finite future expiry; the database rechecks owner authority.
func (h *ComplianceGrantHandler) Create(c *gin.Context) {
	if !requireContentOwner(c) {
		return
	}
	var input struct {
		UserID    int64     `json:"user_id"`
		Scope     string    `json:"scope"`
		Reason    string    `json:"reason"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if c.ShouldBindJSON(&input) != nil || input.UserID <= 0 || (input.Scope != "full_content" && input.Scope != "export" && input.Scope != "retention_manage") || strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 2000 || !input.ExpiresAt.After(time.Now()) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	tid, _ := middleware.GetTenantID(c)
	actor, _ := middleware.GetUserID(c)
	row, err := requestStore(c, h.Store).CreateComplianceGrant(c, store.CreateComplianceGrantParams{TenantID: tid, UserID: input.UserID, Scope: input.Scope, Reason: strings.TrimSpace(input.Reason), ExpiresAt: pgtype.Timestamptz{Time: input.ExpiresAt.UTC(), Valid: true}, GrantedBy: pgtype.Int8{Int64: actor, Valid: true}})
	if err != nil {
		if !respondConstraintError(c, err) {
			authError(c, 409, "INVALID_PARAMS")
		}
		return
	}
	c.JSON(201, gin.H{"grant": row})
}

// Revoke preserves the original grant and issuer; already revoked grants are idempotent.
func (h *ComplianceGrantHandler) Revoke(c *gin.Context) {
	if !requireContentOwner(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	tid, _ := middleware.GetTenantID(c)
	actor, _ := middleware.GetUserID(c)
	row, err := requestStore(c, h.Store).RevokeComplianceGrant(c, store.RevokeComplianceGrantParams{TenantID: tid, ID: id, RevokedBy: pgtype.Int8{Int64: actor, Valid: true}})
	if err != nil {
		authError(c, 404, "NOT_FOUND")
		return
	}
	c.JSON(200, gin.H{"grant": row})
}
