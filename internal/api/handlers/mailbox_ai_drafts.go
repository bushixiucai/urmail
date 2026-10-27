package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/aidraft"
	"github.com/bushixiucai/urmail/internal/store"
)

type aiDraftStore interface {
	PlatformInsertAIDraft(context.Context, store.PlatformInsertAIDraftParams) (store.PlatformInsertAIDraftRow, error)
	PlatformGetAIDraft(context.Context, store.PlatformGetAIDraftParams) (store.PlatformGetAIDraftRow, error)
	PlatformUpdateAIDraft(context.Context, store.PlatformUpdateAIDraftParams) (int64, error)
}

// AIDrafts implements the mailbox AI draft CRUD endpoints on top of the
// platform draft store.
type AIDrafts struct{ Q aiDraftStore }

type aiDraftRequest struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Version int64  `json:"version"`
}

func (h AIDrafts) principal(c *gin.Context) (int64, int64, bool) {
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 || session.MailboxID <= 0 {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return 0, 0, false
	}
	return session.TenantID, session.MailboxID, true
}

// Create parses and stores a new AI draft for the authenticated mailbox.
func (h AIDrafts) Create(c *gin.Context) {
	tenant, mailbox, ok := h.principal(c)
	if !ok {
		return
	}
	var req aiDraftRequest
	if !bindMailboxMessage(c, &req) {
		return
	}
	draft, err := aidraft.Parse(req.Subject, req.Body, 1)
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	row, err := h.Q.PlatformInsertAIDraft(c.Request.Context(), store.PlatformInsertAIDraftParams{TenantID: tenant, MailboxID: mailbox, Subject: draft.Subject, Body: draft.Body})
	if err != nil {
		authError(c, http.StatusBadGateway, "AI_DRAFT_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": row.ID, "subject": draft.Subject, "body": draft.Body, "version": row.Version})
}

// Get returns one stored AI draft belonging to the authenticated mailbox.
func (h AIDrafts) Get(c *gin.Context) {
	tenant, mailbox, ok := h.principal(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	row, err := h.Q.PlatformGetAIDraft(c.Request.Context(), store.PlatformGetAIDraftParams{TenantID: tenant, MailboxID: mailbox, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusNotFound, "AI_DRAFT_NOT_FOUND")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "AI_DRAFT_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": row.ID, "subject": row.Subject, "body": row.Body, "version": row.Version})
}

// Update rewrites an AI draft's content behind an optimistic version check;
// stale versions conflict instead of overwriting newer edits.
func (h AIDrafts) Update(c *gin.Context) {
	tenant, mailbox, ok := h.principal(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	var req aiDraftRequest
	if !bindMailboxMessage(c, &req) || req.Version <= 0 {
		if !c.IsAborted() {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		}
		return
	}
	draft, err := aidraft.Parse(req.Subject, req.Body, req.Version)
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	version, err := h.Q.PlatformUpdateAIDraft(c.Request.Context(), store.PlatformUpdateAIDraftParams{TenantID: tenant, MailboxID: mailbox, ID: id, Subject: draft.Subject, Body: draft.Body, Version: req.Version})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusConflict, "AI_DRAFT_CONFLICT")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "AI_DRAFT_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "subject": draft.Subject, "body": draft.Body, "version": version})
}
