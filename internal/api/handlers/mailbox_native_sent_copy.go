package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/mailboxschedule"
	"github.com/bushixiucai/urmail/internal/store"
)

// RepairScheduledSentCopy uses a fresh authenticated owner session, including
// after a password change. It cannot reach any SMTP submission code.
func (h *MailboxNativeHandler) RepairScheduledSentCopy(c *gin.Context) {
	if !h.requireEnabled(c) {
		return
	}
	if h.Pool == nil {
		authError(c, http.StatusServiceUnavailable, "WEBMAIL_DISABLED")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	// Authenticate IMAP before acquiring a lease, so failed/expired sessions
	// cannot make repair unavailable to the current owner.
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	q := store.New(h.Pool.Pool)
	row, err := q.PlatformClaimOwnedScheduledSentCopy(c.Request.Context(), store.PlatformClaimOwnedScheduledSentCopyParams{TenantID: session.TenantID, MailboxID: session.MailboxID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		current, getErr := q.PlatformGetMailboxScheduledSend(c.Request.Context(), store.PlatformGetMailboxScheduledSendParams{TenantID: session.TenantID, MailboxID: session.MailboxID, ID: id})
		if errors.Is(getErr, pgx.ErrNoRows) {
			authError(c, http.StatusNotFound, "NOT_FOUND")
			return
		}
		if getErr == nil && current.Status == "sent" && current.SentCopyState == "complete" {
			c.JSON(http.StatusOK, gin.H{"item": scheduledSendJSON(current)})
			return
		}
		authError(c, http.StatusConflict, "SENT_COPY_PENDING")
		return
	}
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	if err = mailboxschedule.RepairSentCopyWithAuthority(ctx, q, row, client, h.Secrets, h.copySessionAuthority(session)); err != nil {
		authError(c, http.StatusConflict, "SENT_COPY_PENDING")
		return
	}
	current, err := q.PlatformGetMailboxScheduledSend(c.Request.Context(), store.PlatformGetMailboxScheduledSendParams{TenantID: session.TenantID, MailboxID: session.MailboxID, ID: id})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	detail, _ := json.Marshal(gin.H{"actor_type": "mailbox", "mailbox_id": session.MailboxID, "schedule_id": id, "result": "complete"})
	if _, auditErr := q.CreateAuditLog(c.Request.Context(), store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: session.TenantID, Valid: true}, Action: "mailbox.scheduled_send.sent_copy_repaired", TargetType: pgtype.Text{String: "mailbox_scheduled_send", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(id, 10), Valid: true}, Detail: detail}); auditErr != nil {
		middleware.RecordAuditFailure(true)
	}
	c.JSON(http.StatusOK, gin.H{"item": scheduledSendJSON(current)})
}
