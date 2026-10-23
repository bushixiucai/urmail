package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// ArchiveStore defines the tenant mail management contract.
type ArchiveStore interface {
	ListArchiveEntriesByTenant(ctx context.Context, arg store.ListArchiveEntriesByTenantParams) ([]store.ArchiveEntries, error)
	SetArchiveEntryLegalHold(ctx context.Context, arg store.SetArchiveEntryLegalHoldParams) (store.ArchiveEntries, error)
	ClearArchiveEntryLegalHold(ctx context.Context, arg store.ClearArchiveEntryLegalHoldParams) (store.ArchiveEntries, error)
	SoftDeleteArchiveEntry(ctx context.Context, arg store.SoftDeleteArchiveEntryParams) (int64, error)
}

// ArchiveHandler defines the tenant mail management contract.
type ArchiveHandler struct {
	Store ArchiveStore
}

// NewArchiveHandler handles the tenant mail management request.
func NewArchiveHandler(s ArchiveStore) *ArchiveHandler {
	return &ArchiveHandler{Store: s}
}

// ArchiveResponse defines the tenant mail management contract.
type ArchiveResponse struct {
	ID         int64              `json:"id"`
	MailboxID  pgtype.Int8        `json:"mailbox_id"`
	MessageID  string             `json:"message_id"`
	Subject    pgtype.Text        `json:"subject"`
	FromAddr   pgtype.Text        `json:"from_addr"`
	ToAddr     pgtype.Text        `json:"to_addr"`
	ReceivedAt pgtype.Timestamptz `json:"received_at"`
	SizeBytes  int64              `json:"size_bytes"`
	LegalHold  bool               `json:"legal_hold"`
}

func newArchiveResponse(a store.ArchiveEntries) ArchiveResponse {
	return ArchiveResponse{
		ID:         a.ID,
		MailboxID:  a.MailboxID,
		MessageID:  a.MessageID,
		Subject:    a.Subject,
		FromAddr:   a.FromAddr,
		ToAddr:     a.ToAddr,
		ReceivedAt: a.ReceivedAt,
		SizeBytes:  a.SizeBytes,
		LegalHold:  a.LegalHold,
	}
}

// List handles the tenant mail management request.
func (h *ArchiveHandler) List(c *gin.Context) {
	q := requestStore(c, h.Store)
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	limit := int32(50)
	offset := int32(0)
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 32); err == nil && v > 0 && v <= 200 {
			limit = int32(v)
		}
	}
	if raw := c.Query("offset"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 32); err == nil && v >= 0 {
			offset = int32(v)
		}
	}
	entries, err := q.ListArchiveEntriesByTenant(c, store.ListArchiveEntriesByTenantParams{
		TenantID: tenantID,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	out := make([]ArchiveResponse, 0, len(entries))
	for _, entry := range entries {
		out = append(out, newArchiveResponse(entry))
	}
	c.JSON(http.StatusOK, gin.H{"entries": out})
}

// SetHold handles the tenant mail management request.
func (h *ArchiveHandler) SetHold(c *gin.Context) {
	h.mutateHold(c, true)
}

// ClearHold handles the tenant mail management request.
func (h *ArchiveHandler) ClearHold(c *gin.Context) {
	h.mutateHold(c, false)
}

func (h *ArchiveHandler) mutateHold(c *gin.Context, held bool) {
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
	var entry store.ArchiveEntries
	if held {
		entry, err = q.SetArchiveEntryLegalHold(c, store.SetArchiveEntryLegalHoldParams{TenantID: tenantID, ID: id})
	} else {
		entry, err = q.ClearArchiveEntryLegalHold(c, store.ClearArchiveEntryLegalHoldParams{TenantID: tenantID, ID: id})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND"}})
		return
	}
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"entry": newArchiveResponse(entry)})
}

// Delete handles the tenant mail management request.
func (h *ArchiveHandler) Delete(c *gin.Context) {
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
	deletedID, err := q.SoftDeleteArchiveEntry(c, store.SoftDeleteArchiveEntryParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "LEGAL_HOLD"}})
		return
	}
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	_ = deletedID
	c.JSON(http.StatusOK, gin.H{"status": "ok", "deleted_at": time.Now()})
}
