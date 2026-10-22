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
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/store"
)

type MailboxStore interface {
	ListMailboxesByTenant(ctx context.Context, tenantID int64) ([]store.Mailboxes, error)
	GetMailboxByID(ctx context.Context, arg store.GetMailboxByIDParams) (store.Mailboxes, error)
	GetDomainByID(ctx context.Context, arg store.GetDomainByIDParams) (store.GetDomainByIDRow, error)
	CreateMailbox(ctx context.Context, arg store.CreateMailboxParams) (store.Mailboxes, error)
	UpdateMailboxPassword(ctx context.Context, arg store.UpdateMailboxPasswordParams) (store.Mailboxes, error)
	UpdateMailboxQuota(ctx context.Context, arg store.UpdateMailboxQuotaParams) (store.Mailboxes, error)
	DeleteMailbox(ctx context.Context, arg store.DeleteMailboxParams) error
	CountMailboxesByTenant(ctx context.Context, tenantID int64) (int64, error)
	GetActiveSubscriptionByTenant(ctx context.Context, tenantID int64) (store.Subscriptions, error)
	GetPlanByCode(ctx context.Context, code string) (store.Plans, error)
	ListMailboxesWithLegalHold(ctx context.Context, tenantID int64) ([]pgtype.Int8, error)
}

type MailboxHandler struct {
	Store     MailboxStore
	Reconcile ReconcileTrigger
}

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

func (h *MailboxHandler) List(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	mailboxes, err := q.ListMailboxesByTenant(c, tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}

	items := make([]MailboxResponse, 0, len(mailboxes))
	for _, m := range mailboxes {
		items = append(items, newMailboxResponse(m))
	}

	c.JSON(http.StatusOK, gin.H{"mailboxes": items})
}

type CreateMailboxRequest struct {
	DomainID    int64  `json:"domain_id" binding:"required"`
	LocalPart   string `json:"local_part" binding:"required"`
	Password    string `json:"password" binding:"required,min=6"`
	DisplayName string `json:"display_name"`
	QuotaBytes  int64  `json:"quota_bytes"` // 0 = unlimited or plan default
}

func (h *MailboxHandler) Create(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	var req CreateMailboxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}

	domain, err := q.GetDomainByID(c, store.GetDomainByIDParams{
		TenantID: tenantID,
		ID:       req.DomainID,
	})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "DOMAIN_NOT_FOUND"}})
		return
	}

	local := strings.ToLower(strings.TrimSpace(req.LocalPart))
	if len(local) > 64 || !validation.LocalPartRegex.MatchString(local) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "INVALID_LOCAL_PART", "message": "mailbox.invalid_local_part"},
		})
		return
	}
	if req.QuotaBytes < 0 {
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
	hash, err := domainMail.HashPassword(req.Password)
	if err != nil {
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
		c.JSON(http.StatusConflict, gin.H{
			"error": gin.H{"code": "MAILBOX_CONFLICT", "message": "mailbox.already_exists"},
		})
		return
	}

	if h.Reconcile != nil {
		h.Reconcile.Trigger()
	}

	c.JSON(http.StatusCreated, gin.H{"mailbox": newMailboxResponse(mailbox)})
}

type UpdatePasswordRequest struct {
	Password string `json:"password" binding:"required,min=6"`
}

func (h *MailboxHandler) UpdatePassword(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}

	var req UpdatePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}

	hash, err := domainMail.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "HASH_FAILED"}})
		return
	}

	mailbox, err := q.UpdateMailboxPassword(c, store.UpdateMailboxPasswordParams{
		TenantID:     tenantID,
		ID:           id,
		PasswordHash: hash,
	})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "MAILBOX_NOT_FOUND"}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"mailbox": newMailboxResponse(mailbox)})
}

type UpdateQuotaRequest struct {
	QuotaBytes int64 `json:"quota_bytes" binding:"min=0"`
}

func (h *MailboxHandler) UpdateQuota(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
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
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "MAILBOX_NOT_FOUND"}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"mailbox": newMailboxResponse(mailbox)})
}

func (h *MailboxHandler) Delete(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}

	// Compliance: a mailbox with archive entries under legal hold must not
	// be deleted.
	held, err := mailboxUnderLegalHold(c, q, tenantID, id)
	if err != nil {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DELETE_FAILED"}})
		return
	}

	if h.Reconcile != nil {
		h.Reconcile.Trigger()
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

type UpdateMailboxQuotaRequest struct {
	QuotaBytes *int64 `json:"quota_bytes" binding:"required"`
}

// UpdateQuota changes the per-mailbox storage quota. Dovecot reads the value
// from the userdb query on each session/LMTP delivery, so no config reload is
// required.
func (h *MailboxHandler) UpdateQuota(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	var req UpdateMailboxQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.QuotaBytes == nil || *req.QuotaBytes < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	mailbox, err := q.UpdateMailboxQuota(c, store.UpdateMailboxQuotaParams{
		TenantID:   tenantID,
		ID:         id,
		QuotaBytes: *req.QuotaBytes,
	})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "MAILBOX_NOT_FOUND"}})
		return
	}
	if h.Reconcile != nil {
		h.Reconcile.Trigger()
	}
	c.JSON(http.StatusOK, gin.H{"mailbox": newMailboxResponse(mailbox)})
}
