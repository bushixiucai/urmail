package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// MailboxSnippetsHandler manages canned snippets for the authenticated mailbox (WP05, I16).
type MailboxSnippetsHandler struct {
	Pool *db.Pool
}

type snippetJSON struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	Shortcut  string `json:"shortcut,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type snippetCreateOrUpdateRequest struct {
	Title    string `json:"title"`
	Content  string `json:"content"`
	Shortcut string `json:"shortcut"`
}

func formatSnippet(s store.MailboxSnippets) snippetJSON {
	return snippetJSON{
		ID:        s.ID,
		Title:     s.Title,
		Content:   s.Content,
		Shortcut:  s.Shortcut,
		CreatedAt: s.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		UpdatedAt: s.UpdatedAt.Time.UTC().Format(time.RFC3339Nano),
	}
}

// List returns all personal canned snippets for this mailbox.
func (h *MailboxSnippetsHandler) List(c *gin.Context) {
	if h.Pool == nil {
		authError(c, http.StatusServiceUnavailable, "SNIPPETS_UNAVAILABLE")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 || session.MailboxID <= 0 {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	tx, err := h.Pool.MailboxTx(ctx, session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	rows, err := q.PlatformListMailboxSnippets(ctx, store.PlatformListMailboxSnippetsParams{
		TenantID:  session.TenantID,
		MailboxID: session.MailboxID,
	})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}

	out := make([]snippetJSON, len(rows))
	for i, s := range rows {
		out[i] = formatSnippet(s)
	}
	c.JSON(http.StatusOK, gin.H{"snippets": out})
}

// Create adds a new personal snippet.
func (h *MailboxSnippetsHandler) Create(c *gin.Context) {
	if h.Pool == nil {
		authError(c, http.StatusServiceUnavailable, "SNIPPETS_UNAVAILABLE")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 || session.MailboxID <= 0 {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	var req snippetCreateOrUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "SNIPPET_INVALID")
		return
	}
	title := strings.TrimSpace(req.Title)
	if len(title) == 0 || len(title) > 120 {
		authError(c, http.StatusBadRequest, "SNIPPET_TITLE_INVALID")
		return
	}
	if len(req.Content) > 32768 {
		authError(c, http.StatusBadRequest, "SNIPPET_CONTENT_TOO_LARGE")
		return
	}
	shortcut := strings.TrimSpace(req.Shortcut)
	if len(shortcut) > 64 {
		authError(c, http.StatusBadRequest, "SNIPPET_SHORTCUT_TOO_LONG")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	tx, err := h.Pool.MailboxTx(ctx, session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	row, err := q.PlatformInsertMailboxSnippet(ctx, store.PlatformInsertMailboxSnippetParams{
		TenantID:  session.TenantID,
		MailboxID: session.MailboxID,
		Title:     title,
		Content:   req.Content,
		Shortcut:  shortcut,
	})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, formatSnippet(row))
}

// Update modifies an existing personal snippet.
func (h *MailboxSnippetsHandler) Update(c *gin.Context) {
	if h.Pool == nil {
		authError(c, http.StatusServiceUnavailable, "SNIPPETS_UNAVAILABLE")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 || session.MailboxID <= 0 {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_ID")
		return
	}
	var req snippetCreateOrUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "SNIPPET_INVALID")
		return
	}
	title := strings.TrimSpace(req.Title)
	if len(title) == 0 || len(title) > 120 {
		authError(c, http.StatusBadRequest, "SNIPPET_TITLE_INVALID")
		return
	}
	if len(req.Content) > 32768 {
		authError(c, http.StatusBadRequest, "SNIPPET_CONTENT_TOO_LARGE")
		return
	}
	shortcut := strings.TrimSpace(req.Shortcut)
	if len(shortcut) > 64 {
		authError(c, http.StatusBadRequest, "SNIPPET_SHORTCUT_TOO_LONG")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	tx, err := h.Pool.MailboxTx(ctx, session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	row, err := q.PlatformUpdateMailboxSnippet(ctx, store.PlatformUpdateMailboxSnippetParams{
		TenantID:  session.TenantID,
		MailboxID: session.MailboxID,
		ID:        id,
		Title:     title,
		Content:   req.Content,
		Shortcut:  shortcut,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			authError(c, http.StatusNotFound, "SNIPPET_NOT_FOUND")
			return
		}
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, formatSnippet(row))
}

// Delete removes a personal snippet.
func (h *MailboxSnippetsHandler) Delete(c *gin.Context) {
	if h.Pool == nil {
		authError(c, http.StatusServiceUnavailable, "SNIPPETS_UNAVAILABLE")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 || session.MailboxID <= 0 {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_ID")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	tx, err := h.Pool.MailboxTx(ctx, session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	deleted, err := q.PlatformDeleteMailboxSnippet(ctx, store.PlatformDeleteMailboxSnippetParams{
		TenantID:  session.TenantID,
		MailboxID: session.MailboxID,
		ID:        id,
	})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if deleted == 0 {
		authError(c, http.StatusNotFound, "SNIPPET_NOT_FOUND")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// RegisterMailboxSnippetRoutes wires up the personal snippets CRUD endpoints.
func RegisterMailboxSnippetRoutes(group *gin.RouterGroup, h *MailboxSnippetsHandler) {
	group.GET("/snippets", h.List)
	group.POST("/snippets", h.Create)
	group.PUT("/snippets/:id", h.Update)
	group.DELETE("/snippets/:id", h.Delete)
}
