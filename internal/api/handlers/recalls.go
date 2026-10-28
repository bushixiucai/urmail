package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// RecallExpunger asks the privileged agent to process one persisted local recall.
// The agent resolves and validates the mailbox and message itself.
type RecallExpunger interface {
	ExpungeRecall(context.Context, int64, int64) error
}

// RecallHandler exposes local mailbox recalls without promising remote recall.
type RecallHandler struct {
	AppPool  *db.Pool
	Store    *store.Queries
	Platform *store.Queries
	Expunger RecallExpunger
}

// RecallResponse describes the outcome for one local mailbox copy only.
type RecallResponse struct {
	ID               int64              `json:"id"`
	ArchiveEntryID   pgtype.Int8        `json:"archive_entry_id"`
	MailboxID        pgtype.Int8        `json:"mailbox_id"`
	Status           string             `json:"status"`
	Reason           pgtype.Text        `json:"reason"`
	Scope            string             `json:"scope"`
	CreatedAt        pgtype.Timestamptz `json:"created_at"`
	CompletedAt      pgtype.Timestamptz `json:"completed_at"`
	PreviewToken     pgtype.Text        `json:"preview_token"`
	PreviewDigest    pgtype.Text        `json:"preview_digest"`
	PreviewExpiresAt pgtype.Timestamptz `json:"preview_expires_at"`
	PreviewActorID   pgtype.Int8        `json:"preview_actor_id"`
	AttemptEpoch     int64              `json:"attempt_epoch"`
	SelectedCopyKeys json.RawMessage    `json:"selected_copy_keys"`
	LocalResult      json.RawMessage    `json:"local_result"`
	ResultHistory    json.RawMessage    `json:"result_history"`
}

func recallResponse(r store.RecallRequests) RecallResponse {
	return RecallResponse{ID: r.ID, ArchiveEntryID: r.ArchiveEntryID, MailboxID: r.MailboxID,
		Status: r.Status, Reason: r.Reason, Scope: "local_mailbox", CreatedAt: r.CreatedAt, CompletedAt: r.CompletedAt, PreviewToken: r.PreviewToken, PreviewDigest: r.PreviewDigest, PreviewExpiresAt: r.PreviewExpiresAt, PreviewActorID: r.PreviewActorID, AttemptEpoch: r.AttemptEpoch, SelectedCopyKeys: json.RawMessage(r.SelectedCopyKeys), LocalResult: json.RawMessage(r.LocalResult), ResultHistory: json.RawMessage(r.ResultHistory)}
}

// Create persists a request before the agent can act; duplicates reuse the row.
func (h *RecallHandler) Create(c *gin.Context) { h.confirmReviewed(c) }

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
