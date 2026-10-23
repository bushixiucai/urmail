package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// RecallExpunger asks the privileged agent to process one persisted local recall.
// The agent resolves and validates the mailbox and message itself.
type RecallExpunger interface {
	ExpungeRecall(context.Context, int64, int64) error
}

// RecallHandler exposes local mailbox recalls without promising remote recall.
type RecallHandler struct {
	Store    *store.Queries
	Platform *store.Queries
	Expunger RecallExpunger
}

// RecallResponse describes the outcome for one local mailbox copy only.
type RecallResponse struct {
	ID             int64              `json:"id"`
	ArchiveEntryID pgtype.Int8        `json:"archive_entry_id"`
	MailboxID      pgtype.Int8        `json:"mailbox_id"`
	Status         string             `json:"status"`
	Reason         pgtype.Text        `json:"reason"`
	Scope          string             `json:"scope"`
	CreatedAt      pgtype.Timestamptz `json:"created_at"`
	CompletedAt    pgtype.Timestamptz `json:"completed_at"`
}

func recallResponse(r store.RecallRequests) RecallResponse {
	return RecallResponse{ID: r.ID, ArchiveEntryID: r.ArchiveEntryID, MailboxID: r.MailboxID,
		Status: r.Status, Reason: r.Reason, Scope: "local_mailbox", CreatedAt: r.CreatedAt, CompletedAt: r.CompletedAt}
}

// Create persists a request before the agent can act; duplicates reuse the row.
func (h *RecallHandler) Create(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	userID, userOK := middleware.GetUserID(c)
	if !ok || !userOK {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	var input struct {
		ArchiveEntryID int64 `json:"archive_entry_id" binding:"required,gt=0"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_PARAMS"}})
		return
	}
	q := requestStore(c, h.Store)
	target, err := q.GetRecallTarget(c.Request.Context(), store.GetRecallTargetParams{TenantID: tenantID, ID: input.ArchiveEntryID})
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND"}})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if target.LegalHold {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "LEGAL_HOLD"}})
		return
	}
	row, err := q.CreateRecall(c.Request.Context(), store.CreateRecallParams{TenantID: tenantID,
		ArchiveEntryID: pgtype.Int8{Int64: input.ArchiveEntryID, Valid: true}, MailboxID: target.MailboxID,
		MessageID: target.MessageID, RequestedBy: userID})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	// No response is written until the transaction commits: a commit failure
	// must never publish an accepted recall or invoke a destructive operation.
	middleware.AfterCommit(c, func() {
		if row.Status == "pending" && h.Expunger != nil {
			// An ambiguous transport failure leaves a durable pending request.
			// A duplicate POST retries the same idempotent agent operation.
			_ = h.Expunger.ExpungeRecall(c.Request.Context(), tenantID, row.ID)
		}
		current, err := h.Platform.GetRecallByID(c.Request.Context(), store.GetRecallByIDParams{TenantID: tenantID, ID: row.ID})
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "DB_ERROR"}, "recall_id": row.ID})
			return
		}
		status := http.StatusOK
		if current.Status == "pending" {
			status = http.StatusAccepted
		}
		c.JSON(status, gin.H{"recall": recallResponse(current)})
	})
}

// List returns at most the latest hundred tenant recalls.
func (h *RecallHandler) List(c *gin.Context) {
	tenantID, _ := middleware.GetTenantID(c)
	rows, err := requestStore(c, h.Store).ListRecalls(c.Request.Context(), tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	items := make([]RecallResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, recallResponse(row))
	}
	c.JSON(http.StatusOK, gin.H{"recalls": items, "scope": "local_mailbox"})
}

// Get returns one recall without exposing another tenant's target.
func (h *RecallHandler) Get(c *gin.Context) {
	tenantID, _ := middleware.GetTenantID(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ID"}})
		return
	}
	row, err := requestStore(c, h.Store).GetRecallByID(c.Request.Context(), store.GetRecallByIDParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND"}})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"recall": recallResponse(row)})
}
