package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/mailboxsend"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// Scheduling only references an already saved draft. Creating an IMAP message
// and a durable delivery intention cannot be one atomic transaction.
type mailboxScheduleRequest struct {
	DraftUID         uint32 `json:"draft_uid"`
	DraftUIDValidity uint32 `json:"draft_uidvalidity"`
	SendAt           string `json:"send_at"`
}

func validScheduleKey(key string) bool {
	if len(key) < 16 || len(key) > 128 {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			continue
		default:
			return false
		}
	}
	return true
}

func scheduleRequestHash(req mailboxScheduleRequest, at time.Time) string {
	raw, _ := json.Marshal(struct {
		UID      uint32
		Validity uint32
		At       string
	}{req.DraftUID, req.DraftUIDValidity, at.UTC().Format(time.RFC3339Nano)})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// CreateScheduledSend records one idempotent intention for an existing IMAP draft.
func (h *MailboxNativeHandler) CreateScheduledSend(c *gin.Context) {
	if !h.requireEnabled(c) {
		return
	}
	if h.Pool == nil || h.Secrets == nil {
		authError(c, http.StatusServiceUnavailable, "WEBMAIL_DISABLED")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if !validScheduleKey(key) {
		authError(c, http.StatusBadRequest, "SCHEDULE_IDEMPOTENCY_REQUIRED")
		return
	}
	var req mailboxScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.DraftUID == 0 || req.DraftUIDValidity == 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	sendAt, err := time.Parse(time.RFC3339Nano, req.SendAt)
	if err != nil || sendAt.IsZero() {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	sendAt = sendAt.UTC()
	digest := scheduleRequestHash(req, sendAt)
	q := store.New(h.Pool.Pool)
	keyArg := store.PlatformGetMailboxScheduledSendByKeyParams{TenantID: session.TenantID, MailboxID: session.MailboxID, IdempotencyKey: key}
	existing, err := q.PlatformGetMailboxScheduledSendByKey(c.Request.Context(), keyArg)
	if err == nil {
		h.returnScheduleReplay(c, existing, digest)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if !sendAt.After(time.Now().UTC().Add(30 * time.Second)) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	password, ok := h.vaultPassword(c)
	if !ok {
		return
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	folder, err := client.EnsureDraftsFolder(c.Request.Context())
	if err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	validity, err := client.FolderUIDValidity(c.Request.Context(), folder)
	if err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	if validity != req.DraftUIDValidity {
		authError(c, http.StatusConflict, "DRAFT_CHANGED")
		return
	}
	outgoing, draftErr := webmail.OutgoingFromDraft(c.Request.Context(), client, folder, req.DraftUID)
	if draftErr != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if _, _, ok := h.authorizedIdentity(c, session, outgoing.From); !ok {
		return
	}
	sealed, err := mailboxsend.Seal(h.Secrets, password)
	if err != nil || sealed == "" {
		authError(c, http.StatusServiceUnavailable, "WEBMAIL_DISABLED")
		return
	}
	tx, err := h.Pool.Begin(c.Request.Context())
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	lockedQ := store.New(tx)
	if err = mailboxsend.LockDraft(c, lockedQ, session.TenantID, session.MailboxID, mailboxsend.DraftRef{Folder: folder, UID: req.DraftUID, Validity: validity}); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	locked, err := lockedQ.PlatformDraftSendLocked(c, store.PlatformDraftSendLockedParams{TenantID: session.TenantID, MailboxID: session.MailboxID, DraftFolder: folder, DraftUid: int64(req.DraftUID), DraftUidvalidity: int64(validity)})
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if locked.Bool {
		if replay, e := lockedQ.PlatformGetMailboxScheduledSendByKey(c.Request.Context(), keyArg); e == nil {
			h.returnScheduleReplay(c, replay, digest)
			return
		}
		authError(c, 409, "DRAFT_SEND_LOCKED")
		return
	}
	// Autosave may have replaced the source while the first IMAP read was in flight.
	if current, e := client.FolderUIDValidity(c.Request.Context(), folder); e != nil || current != validity {
		authError(c, 409, "DRAFT_CHANGED")
		return
	}
	if _, e := client.GetMessage(c.Request.Context(), folder, req.DraftUID); e != nil {
		authError(c, 409, "DRAFT_CHANGED")
		return
	}
	row, err := lockedQ.PlatformCreateMailboxScheduledSend(c.Request.Context(), store.PlatformCreateMailboxScheduledSendParams{
		TenantID: session.TenantID, MailboxID: session.MailboxID, DraftFolder: folder, DraftUid: int64(req.DraftUID),
		DraftUidvalidity: int64(validity), SendAt: pgtype.Timestamptz{Time: sendAt, Valid: true}, SmtpSecret: sealed, IdempotencyKey: key, RequestHash: digest,
	})
	if err != nil {
		_ = tx.Rollback(context.Background())
	}
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23505" {
			// A simultaneous identical request may have committed while we checked IMAP.
			if row, err = q.PlatformGetMailboxScheduledSendByKey(c.Request.Context(), keyArg); err == nil {
				h.returnScheduleReplay(c, row, digest)
				return
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				authError(c, http.StatusServiceUnavailable, "DB_ERROR")
				return
			}
			authError(c, http.StatusConflict, "SCHEDULE_CONFLICT")
			return
		}
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	detail, _ := json.Marshal(gin.H{"actor_type": "mailbox", "mailbox_id": session.MailboxID, "schedule_id": row.ID})
	if _, err = q.CreateAuditLog(c.Request.Context(), store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: session.TenantID, Valid: true}, Action: "mailbox.scheduled_send.created", TargetType: pgtype.Text{String: "mailbox_scheduled_send", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(row.ID, 10), Valid: true}, Detail: detail}); err != nil {
		middleware.RecordAuditFailure(true)
	}
	c.JSON(http.StatusOK, scheduledSendJSON(row))
}

func (h *MailboxNativeHandler) returnScheduleReplay(c *gin.Context, row store.MailboxScheduledSends, digest string) {
	if row.RequestHash != digest {
		authError(c, http.StatusConflict, "SCHEDULE_CONFLICT")
		return
	}
	c.JSON(http.StatusOK, scheduledSendJSON(row))
}

// ListScheduledSends includes terminal results so disappearance is never
// confused with successful delivery. Pagination is scoped to this mailbox.
func (h *MailboxNativeHandler) ListScheduledSends(c *gin.Context) {
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
	limit, _ := strconv.ParseInt(c.Query("limit"), 10, 32)
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	offset, _ := strconv.ParseInt(c.Query("offset"), 10, 32)
	if offset < 0 {
		offset = 0
	}
	if offset > 100000 {
		offset = 100000
	}
	rows, err := store.New(h.Pool.Pool).PlatformListMailboxScheduledSends(c.Request.Context(), store.PlatformListMailboxScheduledSendsParams{TenantID: session.TenantID, MailboxID: session.MailboxID, PageLimit: int32(limit), PageOffset: int32(offset)})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		out = append(out, scheduledSendJSON(row))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "limit": limit, "offset": offset})
}

// CancelScheduledSend atomically cancels work before its submission boundary.
func (h *MailboxNativeHandler) CancelScheduledSend(c *gin.Context) {
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
	q := store.New(h.Pool.Pool)
	row, err := q.PlatformCancelMailboxScheduledSend(c.Request.Context(), store.PlatformCancelMailboxScheduledSendParams{TenantID: session.TenantID, MailboxID: session.MailboxID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = q.PlatformGetMailboxScheduledSend(c.Request.Context(), store.PlatformGetMailboxScheduledSendParams{TenantID: session.TenantID, MailboxID: session.MailboxID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			authError(c, http.StatusNotFound, "NOT_FOUND")
			return
		}
		if err == nil {
			authError(c, http.StatusConflict, "SCHEDULE_CANNOT_CANCEL")
			return
		}
	}
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if sealer, ok := h.Vault.(webmail.ScheduleSealer); ok {
		sealer.DeleteSchedule(row.ID)
	}
	detail, _ := json.Marshal(gin.H{"actor_type": "mailbox", "mailbox_id": session.MailboxID, "schedule_id": row.ID})
	if _, err = q.CreateAuditLog(c.Request.Context(), store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: session.TenantID, Valid: true}, Action: "mailbox.scheduled_send.cancelled", TargetType: pgtype.Text{String: "mailbox_scheduled_send", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(row.ID, 10), Valid: true}, Detail: detail}); err != nil {
		middleware.RecordAuditFailure(true)
	}
	c.JSON(http.StatusOK, gin.H{"cancelled": true, "item": scheduledSendJSON(row)}) //nolint:misspell // Keep the existing API response key.
}

func scheduledSendJSON(v store.MailboxScheduledSends) gin.H {
	out := gin.H{"sent_copy_state": v.SentCopyState, "id": v.ID, "draft_folder": v.DraftFolder, "draft_uid": v.DraftUid, "draft_uidvalidity": v.DraftUidvalidity, "send_at": timestamptzTime(v.SendAt).UTC().Format(time.RFC3339Nano), "status": v.Status, "attempts": v.Attempts, "created_at": timestamptzTime(v.CreatedAt).UTC().Format(time.RFC3339Nano), "updated_at": timestamptzTime(v.UpdatedAt).UTC().Format(time.RFC3339Nano)}
	if v.Status == "pending" || v.Status == "preparing" {
		out["next_attempt_at"] = timestamptzTime(v.NextAttemptAt).UTC().Format(time.RFC3339Nano)
	}
	if v.SentAt.Valid {
		out["sent_at"] = v.SentAt.Time.UTC().Format(time.RFC3339Nano)
	}
	if v.LastError.Valid && v.LastError.String != "" {
		out["last_error"] = v.LastError.String
	}
	return out
}

// AcknowledgeUncertainScheduledSend releases only an explicitly reviewed uncertain attempt.
func (h *MailboxNativeHandler) AcknowledgeUncertainScheduledSend(c *gin.Context) {
	if !h.requireEnabled(c) {
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	if h.Pool == nil {
		authError(c, 503, "WEBMAIL_DISABLED")
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	var req struct {
		Confirmed bool `json:"confirmed"`
	}
	if e != nil || id <= 0 || c.ShouldBindJSON(&req) != nil || !req.Confirmed {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	row, e := store.New(h.Pool.Pool).PlatformAcknowledgeUncertainScheduledSend(c, store.PlatformAcknowledgeUncertainScheduledSendParams{TenantID: session.TenantID, MailboxID: session.MailboxID, ID: id})
	if errors.Is(e, pgx.ErrNoRows) {
		authError(c, 409, "SCHEDULE_CONFLICT")
		return
	}
	if e != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.JSON(200, scheduledSendJSON(row))
}
