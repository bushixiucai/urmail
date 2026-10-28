package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type transferReceipt struct {
	Digest   string                 `json:"digest"`
	Response webmail.TransferResult `json:"response"`
}

func transferDigest(operation, folder string, req mailboxTransferRequest) string {
	raw, _ := json.Marshal([]any{operation, folder, req})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// transferJournal commits an uncertain receipt before any IMAP side effect.
// A lost response can therefore never turn into an automatic duplicate COPY.
func (h *MailboxNativeHandler) transferJournal(c *gin.Context, key, digest string, claim bool) bool {
	session, ok := middleware.GetMailboxSession(c)
	if !ok || h.Pool == nil {
		authError(c, 503, "WEBMAIL_DISABLED")
		return false
	}
	ctx := c.Request.Context()
	tx, err := h.Pool.MailboxTx(ctx, session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return false
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if claim {
		err = q.PlatformLockDataImport(ctx, store.PlatformLockDataImportParams{TenantID: strconv.FormatInt(session.TenantID, 10), MailboxID: strconv.FormatInt(session.MailboxID, 10), IdempotencyKey: "transfer:" + key})
		if err != nil {
			authError(c, 503, "DB_ERROR")
			return false
		}
	}
	row, err := q.PlatformGetDataImportReceipt(ctx, store.PlatformGetDataImportReceiptParams{TenantID: session.TenantID, MailboxID: session.MailboxID, Kind: "mail_transfer", IdempotencyKey: key})
	if err == nil {
		var saved transferReceipt
		if json.Unmarshal(row.Payload, &saved) != nil {
			authError(c, 503, "DB_ERROR")
			return false
		}
		if saved.Digest != digest {
			authError(c, 409, "IDEMPOTENCY_CONFLICT")
			return false
		}
		if row.Status != "done" {
			authError(c, 409, "MAILBOX_SEND_UNCERTAIN")
			return false
		}
		c.JSON(200, saved.Response)
		return false
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		authError(c, 503, "DB_ERROR")
		return false
	}
	if !claim {
		return true
	}
	raw, _ := json.Marshal(transferReceipt{Digest: digest})
	err = q.PlatformClaimMailTransfer(ctx, store.PlatformClaimMailTransferParams{TenantID: session.TenantID, MailboxID: session.MailboxID, IdempotencyKey: key, Payload: raw})
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return false
	}
	if tx.Commit(ctx) != nil {
		authError(c, 503, "DB_ERROR")
		return false
	}
	return true
}

func (h *MailboxNativeHandler) finishTransferJournal(c *gin.Context, key, digest string, result webmail.TransferResult, certain bool) bool {
	session, ok := middleware.GetMailboxSession(c)
	if !ok || h.Pool == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := h.Pool.MailboxTx(ctx, session.TenantID, session.MailboxID)
	if err != nil {
		return false
	}
	defer func() { _ = tx.Rollback(ctx) }()
	raw, _ := json.Marshal(transferReceipt{Digest: digest, Response: result})
	status := "uncertain"
	if certain {
		status = "done"
	}
	count, err := store.New(tx).PlatformCompleteMailTransfer(ctx, store.PlatformCompleteMailTransferParams{TenantID: session.TenantID, MailboxID: session.MailboxID, IdempotencyKey: key, Payload: raw, Status: status})
	return err == nil && count == 1 && tx.Commit(ctx) == nil
}
