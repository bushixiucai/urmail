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
	"github.com/bushixiucai/urmail/internal/mailboxsend"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func (h *MailboxNativeHandler) sendReceiptService() mailboxsend.Service {
	return mailboxsend.Service{Pool: h.Pool, Secrets: h.Secrets, Dialer: h.Dialer, Submitter: h.Submitter}
}
func receiptJSON(row store.MailboxSendReceipts) gin.H {
	out := gin.H{"id": row.ID, "request_key": row.RequestKey, "status": row.Status, "sent_copy_state": row.SentCopyState, "message_id": row.MessageID, "created_at": row.CreatedAt.Time, "last_error": row.LastError, "draft_released": row.DraftReleased}
	if row.AcceptedAt.Valid {
		out["accepted_at"] = row.AcceptedAt.Time
	}
	if row.DraftUid > 0 && !row.DraftReleased {
		out["draft_source"] = gin.H{"folder_enc": webmail.EncodeFolderName(row.DraftFolder), "uid": row.DraftUid, "uid_validity": row.DraftUidvalidity}
	}
	return out
}
func returnSendReceipt(c *gin.Context, row store.MailboxSendReceipts) {
	response := gin.H{"receipt": receiptJSON(row), "sent": row.Status == "accepted"}
	if row.Status == "accepted" {
		response["sent_copy"] = "pending"
		if row.SentCopyState == "complete" {
			response["sent_copy"] = "saved"
		}
	}
	c.JSON(http.StatusOK, response)
}

// ListSendReceipts returns owner-scoped delivery metadata without recovery secrets.
func (h *MailboxNativeHandler) ListSendReceipts(c *gin.Context) {
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
	q := store.New(h.Pool.Pool)
	items := []gin.H{}
	if key := strings.TrimSpace(c.Query("request_key")); key != "" {
		row, e := q.PlatformGetSendReceiptByKey(c.Request.Context(), store.PlatformGetSendReceiptByKeyParams{TenantID: session.TenantID, MailboxID: session.MailboxID, RequestKey: key})
		if e == nil {
			items = append(items, receiptJSON(row))
		} else if !errors.Is(e, pgx.ErrNoRows) {
			authError(c, 503, "DB_ERROR")
			return
		}
	} else if folderEnc := c.Query("folder_enc"); folderEnc != "" {
		folder, e := webmail.DecodeFolderName(folderEnc)
		uid, ue := strconv.ParseUint(c.Query("uid"), 10, 32)
		validity, ve := strconv.ParseUint(c.Query("uid_validity"), 10, 32)
		if e != nil || ue != nil || ve != nil || uid == 0 || validity == 0 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		rows, e := q.PlatformGetSendReceiptsForDraft(c.Request.Context(), store.PlatformGetSendReceiptsForDraftParams{TenantID: session.TenantID, MailboxID: session.MailboxID, DraftFolder: folder, DraftUid: int64(uid), DraftUidvalidity: int64(validity)})
		if e != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
		for _, row := range rows {
			items = append(items, receiptJSON(row))
		}
	} else {
		limit, _ := strconv.ParseInt(c.Query("limit"), 10, 32)
		if limit <= 0 || limit > 100 {
			limit = 50
		}
		offset, _ := strconv.ParseInt(c.Query("offset"), 10, 32)
		if offset < 0 {
			offset = 0
		}
		if offset > 100000 {
			offset = 100000
		}
		rows, e := q.PlatformListSendReceipts(c.Request.Context(), store.PlatformListSendReceiptsParams{TenantID: session.TenantID, MailboxID: session.MailboxID, PageLimit: int32(limit), PageOffset: int32(offset)})
		if e != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
		for _, row := range rows {
			items = append(items, receiptJSON(row))
		}
	}
	c.JSON(200, gin.H{"items": items})
}

// GetSendReceipt resolves one owner-scoped immediate-send result.
func (h *MailboxNativeHandler) GetSendReceipt(c *gin.Context) {
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
	if e != nil || id <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	row, e := store.New(h.Pool.Pool).PlatformGetSendReceipt(c.Request.Context(), store.PlatformGetSendReceiptParams{TenantID: session.TenantID, MailboxID: session.MailboxID, ID: id})
	if errors.Is(e, pgx.ErrNoRows) {
		authError(c, 404, "NOT_FOUND")
		return
	}
	if e != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"receipt": receiptJSON(row)})
}

// RepairSendReceiptCopy uses the current owner credential and never submits SMTP.
func (h *MailboxNativeHandler) RepairSendReceiptCopy(c *gin.Context) {
	if !h.requireEnabled(c) {
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	if h.Pool == nil || h.Secrets == nil {
		authError(c, 503, "WEBMAIL_DISABLED")
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	copyCtx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	service := h.sendReceiptService()
	row, e := service.ClaimOwner(copyCtx, session.TenantID, session.MailboxID, id)
	if e == nil {
		_ = service.Repair(copyCtx, row, client, h.copySessionAuthority(session))
	} else if !errors.Is(e, pgx.ErrNoRows) {
		authError(c, 503, "DB_ERROR")
		return
	}
	row, e = store.New(h.Pool.Pool).PlatformGetSendReceipt(c.Request.Context(), store.PlatformGetSendReceiptParams{TenantID: session.TenantID, MailboxID: session.MailboxID, ID: id})
	if errors.Is(e, pgx.ErrNoRows) {
		authError(c, 404, "NOT_FOUND")
		return
	}
	if e != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"receipt": receiptJSON(row)})
}

// draftSubmissionLocked is also used by autosave/discard to prevent resending a submitted source.
func (h *MailboxNativeHandler) draftSubmissionLocked(c *gin.Context, folder string, uid, validity uint32, queries ...*store.Queries) bool {
	if h.Pool == nil || uid == 0 {
		return false
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return true
	}
	q := store.New(h.Pool.Pool)
	if len(queries) > 0 && queries[0] != nil {
		q = queries[0]
	}
	locked, e := q.PlatformDraftSendLocked(c.Request.Context(), store.PlatformDraftSendLockedParams{TenantID: session.TenantID, MailboxID: session.MailboxID, DraftFolder: folder, DraftUid: int64(uid), DraftUidvalidity: int64(validity)})
	if e != nil {
		authError(c, 503, "DB_ERROR")
		return true
	}
	if locked.Bool {
		authError(c, 409, "DRAFT_SEND_LOCKED")
		return true
	}
	return false
}

// draftDeletionLocked permits explicit cleanup only after the Sent copy is confirmed.
func (h *MailboxNativeHandler) draftDeletionLocked(c *gin.Context, folder string, uid, validity uint32, queries ...*store.Queries) bool {
	if h.Pool == nil || uid == 0 {
		return false
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return true
	}
	q := store.New(h.Pool.Pool)
	if len(queries) > 0 && queries[0] != nil {
		q = queries[0]
	}
	locked, e := q.PlatformDraftDeleteLocked(c.Request.Context(), store.PlatformDraftDeleteLockedParams{TenantID: session.TenantID, MailboxID: session.MailboxID, DraftFolder: folder, DraftUid: int64(uid), DraftUidvalidity: int64(validity)})
	if e != nil {
		authError(c, 503, "DB_ERROR")
		return true
	}
	if locked.Bool {
		authError(c, 409, "DRAFT_SEND_LOCKED")
		return true
	}
	return false
}

func (h *MailboxNativeHandler) copySessionAuthority(session webmail.Session) func(context.Context) error {
	return func(ctx context.Context) error {
		current, err := h.Sessions.Get(ctx, session.TokenHash)
		if err != nil {
			return err
		}
		if current.TenantID != session.TenantID || current.MailboxID != session.MailboxID || current.Address != session.Address {
			return pgx.ErrNoRows
		}
		return nil
	}
}
