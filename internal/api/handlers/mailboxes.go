package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/domain/passwordwork"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/store"
)

// MailboxStore defines the tenant mail management contract.
type MailboxStore interface {
	ListMailboxesByTenant(ctx context.Context, tenantID int64) ([]store.Mailboxes, error)
	GetMailboxByID(ctx context.Context, arg store.GetMailboxByIDParams) (store.Mailboxes, error)
	GetDomainByID(ctx context.Context, arg store.GetDomainByIDParams) (store.GetDomainByIDRow, error)
	CreateMailbox(ctx context.Context, arg store.CreateMailboxParams) (store.Mailboxes, error)
	UpdateMailboxPassword(ctx context.Context, arg store.UpdateMailboxPasswordParams) (store.Mailboxes, error)
	UpdateMailboxQuota(ctx context.Context, arg store.UpdateMailboxQuotaParams) (store.Mailboxes, error)
	UpdateMailboxSettings(ctx context.Context, arg store.UpdateMailboxSettingsParams) (store.UpdateMailboxSettingsRow, error)
	DeleteMailbox(ctx context.Context, arg store.DeleteMailboxParams) error
	CountMailboxesByTenant(ctx context.Context, tenantID int64) (int64, error)
	ListMailboxesWithLegalHold(ctx context.Context, tenantID int64) ([]pgtype.Int8, error)
}

// MailboxHandler defines the tenant mail management contract.
type MailboxHandler struct {
	Store     MailboxStore
	Reconcile ReconcileTrigger
	Vault     interface{ DeleteMailbox(int64, int64) }
}

// NewMailboxHandler handles the tenant mail management request.
func NewMailboxHandler(s MailboxStore, r ReconcileTrigger) *MailboxHandler {
	return &MailboxHandler{Store: s, Reconcile: r}
}

// MailboxResponse is the explicit public shape of a mailbox. The password
// hash and the Sieve script stay server-side; display_name is flattened from
// the nullable column into a plain string or null.
type MailboxResponse struct {
	ID          int64              `json:"id"`
	DomainID    int64              `json:"domain_id"`
	LocalPart   string             `json:"local_part"`
	Address     string             `json:"address"`
	DisplayName *string            `json:"display_name"`
	QuotaBytes  int64              `json:"quota_bytes"`
	Status      string             `json:"status"`
	SendEnabled bool               `json:"send_enabled"`
	CreatedAt   pgtype.Timestamptz `json:"created_at"`
}

func newMailboxResponse(m store.Mailboxes) MailboxResponse {
	return MailboxResponse{
		ID:          m.ID,
		DomainID:    m.DomainID,
		LocalPart:   m.LocalPart,
		Address:     m.Address,
		DisplayName: textPtr(m.DisplayName),
		QuotaBytes:  m.QuotaBytes,
		Status:      m.Status,
		SendEnabled: m.SendEnabled,
		CreatedAt:   m.CreatedAt,
	}
}

// List handles the tenant mail management request.
func (h *MailboxHandler) List(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	mailboxes, err := q.ListMailboxesByTenant(c, tenantID)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}

	items := make([]MailboxResponse, 0, len(mailboxes))
	for _, m := range mailboxes {
		items = append(items, newMailboxResponse(m))
	}

	c.JSON(http.StatusOK, gin.H{"mailboxes": items})
}

// CreateMailboxRequest defines the tenant mail management contract.
type CreateMailboxRequest struct {
	DomainID    int64  `json:"domain_id" binding:"required"`
	LocalPart   string `json:"local_part" binding:"required"`
	Password    string `json:"password" binding:"required,min=12,max=256"`
	DisplayName string `json:"display_name"`
	QuotaBytes  int64  `json:"quota_bytes"` // 0 = unlimited or plan default
}

// Create handles the tenant mail management request.
func (h *MailboxHandler) Create(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	var req CreateMailboxRequest
	if err := c.ShouldBindJSON(&req); err != nil || passwordwork.ValidateNew(req.Password) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}

	domain, err := q.GetDomainByID(c, store.GetDomainByIDParams{
		TenantID: tenantID,
		ID:       req.DomainID,
	})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "DOMAIN_NOT_FOUND"}})
		return
	}

	local := strings.ToLower(strings.TrimSpace(req.LocalPart))
	if len(local) > 64 || !validation.LocalPartRegex.MatchString(local) || strings.ContainsAny(local, "*?/\\") || local == "." || local == ".." {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "INVALID_LOCAL_PART", "message": "mailbox.invalid_local_part"},
		})
		return
	}
	if req.QuotaBytes < -1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	if len(req.DisplayName) > 255 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}

	if !quotaCheck(c, requestStore[QuotaStore](c, nil), tenantID, "mailbox") {
		return
	}

	address := fmt.Sprintf("%s@%s", local, domain.Fqdn)
	hash, err := domainMail.HashPasswordContext(c.Request.Context(), req.Password)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "HASH_FAILED"}})
		return
	}

	mailbox, err := q.CreateMailbox(c, store.CreateMailboxParams{
		TenantID:     tenantID,
		DomainID:     domain.ID,
		LocalPart:    local,
		Address:      address,
		DisplayName:  pgtype.Text{String: req.DisplayName, Valid: req.DisplayName != ""},
		PasswordHash: hash,
		QuotaBytes:   req.QuotaBytes,
		Status:       "active",
		SendEnabled:  true,
	})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusConflict, gin.H{
			"error": gin.H{"code": "MAILBOX_CONFLICT", "message": "mailbox.already_exists"},
		})
		return
	}

	if h.Reconcile != nil {
		middleware.AfterCommit(c, h.Reconcile.Trigger)
	}

	c.JSON(http.StatusCreated, gin.H{"mailbox": newMailboxResponse(mailbox)})
}

// UpdatePasswordRequest defines the tenant mail management contract.
type UpdatePasswordRequest struct {
	Password string `json:"password" binding:"required,min=12,max=256"`
}

// UpdatePassword handles the tenant mail management request.
func (h *MailboxHandler) UpdatePassword(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}

	var req UpdatePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil || passwordwork.ValidateNew(req.Password) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}

	hash, err := domainMail.HashPasswordContext(c.Request.Context(), req.Password)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "HASH_FAILED"}})
		return
	}

	mailbox, err := q.UpdateMailboxPassword(c, store.UpdateMailboxPasswordParams{
		TenantID:     tenantID,
		ID:           id,
		PasswordHash: hash,
	})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "MAILBOX_NOT_FOUND"}})
		return
	}

	if h.Vault != nil {
		middleware.AfterCommit(c, func() { h.Vault.DeleteMailbox(tenantID, id) })
	}
	c.JSON(http.StatusOK, gin.H{"mailbox": newMailboxResponse(mailbox)})
}

// UpdateQuotaRequest defines the tenant mail management contract.
type UpdateQuotaRequest struct {
	QuotaBytes int64 `json:"quota_bytes" binding:"min=-1"`
}

// UpdateQuota handles the tenant mail management request.
func (h *MailboxHandler) UpdateQuota(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}

	var req UpdateQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}

	mailbox, err := q.UpdateMailboxQuota(c, store.UpdateMailboxQuotaParams{
		TenantID:   tenantID,
		ID:         id,
		QuotaBytes: req.QuotaBytes,
	})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "MAILBOX_NOT_FOUND"}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"mailbox": newMailboxResponse(mailbox)})
}

// Delete handles the tenant mail management request.
func (h *MailboxHandler) Delete(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}

	// Compliance: a mailbox with archive entries under legal hold must not
	// be deleted.
	held, err := mailboxUnderLegalHold(c, q, tenantID, id)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if held {
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{"code": "LEGAL_HOLD", "message": "error.LEGAL_HOLD"},
		})
		return
	}

	if err := q.DeleteMailbox(c, store.DeleteMailboxParams{
		TenantID: tenantID,
		ID:       id,
	}); err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DELETE_FAILED"}})
		return
	}

	if h.Reconcile != nil {
		middleware.AfterCommit(c, h.Reconcile.Trigger)
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// UpdateMailboxSettingsRequest defines the tenant mail management contract.
type UpdateMailboxSettingsRequest struct {
	DisplayName *string `json:"display_name"`
	Status      string  `json:"status"`
	SendEnabled *bool   `json:"send_enabled"`
}

// UpdateSettings handles the tenant mail management request.
func (h *MailboxHandler) UpdateSettings(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	current, err := q.GetMailboxByID(c, store.GetMailboxByIDParams{TenantID: tenantID, ID: id})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "MAILBOX_NOT_FOUND"}})
		return
	}
	var req UpdateMailboxSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	status := req.Status
	if status == "" {
		status = current.Status
	}
	if status != "active" && status != "disabled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	display := current.DisplayName
	if req.DisplayName != nil {
		value := strings.TrimSpace(*req.DisplayName)
		switch {
		case value == "":
			display = pgtype.Text{}
		case len(value) > 255:
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
			return
		default:
			display = pgtype.Text{String: value, Valid: true}
		}
	}
	sendEnabled := current.SendEnabled
	if req.SendEnabled != nil {
		sendEnabled = *req.SendEnabled
	}
	updated, err := q.UpdateMailboxSettings(c, store.UpdateMailboxSettingsParams{
		TenantID: tenantID, ID: id, DisplayName: display, Status: status, SendEnabled: sendEnabled,
	})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if h.Reconcile != nil {
		middleware.AfterCommit(c, h.Reconcile.Trigger)
	}
	c.JSON(http.StatusOK, gin.H{"mailbox": MailboxResponse{
		ID: updated.ID, DomainID: updated.DomainID, LocalPart: updated.LocalPart,
		Address: updated.Address, DisplayName: textPtr(updated.DisplayName),
		QuotaBytes: updated.QuotaBytes, Status: updated.Status,
		SendEnabled: updated.SendEnabled, CreatedAt: updated.CreatedAt,
	}})
}
