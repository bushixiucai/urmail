package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bushixiucai/urmail/internal/mailboxsend"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type draftSaveReceipt struct {
	Digest           string                  `json:"digest"`
	MessageID        string                  `json:"message_id"`
	RawDigest        string                  `json:"raw_digest"`
	PreviousUID      uint32                  `json:"previous_uid"`
	PreviousValidity uint32                  `json:"previous_validity"`
	Result           webmail.DraftSaveResult `json:"result"`
}

// lockDraftEdit uses the same PostgreSQL lock as submission. It spans the IMAP
// mutation; the uncertain journal is committed independently before APPEND.
func (h *MailboxNativeHandler) lockDraftEdit(c *gin.Context, session webmail.Session, folder string, uid, validity uint32) (pgx.Tx, bool) {
	if h.Pool == nil {
		authError(c, 503, "DB_ERROR")
		return nil, false
	}
	tx, err := h.Pool.MailboxTx(c.Request.Context(), session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return nil, false
	}
	if err = mailboxsend.LockDraft(c.Request.Context(), store.New(tx), session.TenantID, session.MailboxID, mailboxsend.DraftRef{Folder: folder, UID: uid, Validity: validity}); err != nil {
		_ = tx.Rollback(context.Background())
		authError(c, 503, "DB_ERROR")
		return nil, false
	}
	return tx, true
}

// A session lock spans two committed transactions on one acquired connection.
// It shares its key with send's transaction lock and is always explicitly released.
func (h *MailboxNativeHandler) acquireDraftJournal(c *gin.Context, session webmail.Session, folder string, uid, validity uint32) (*pgxpool.Conn, func(), bool) {
	if h.Pool == nil {
		authError(c, 503, "DB_ERROR")
		return nil, nil, false
	}
	conn, err := h.Pool.Acquire(c.Request.Context())
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return nil, nil, false
	}
	args := store.PlatformAcquireDraftSessionLockParams{TenantID: strconv.FormatInt(session.TenantID, 10), MailboxID: strconv.FormatInt(session.MailboxID, 10), Folder: folder, Uid: strconv.FormatUint(uint64(uid), 10), Validity: strconv.FormatUint(uint64(validity), 10)}
	if err = store.New(conn).PlatformAcquireDraftSessionLock(c.Request.Context(), args); err != nil {
		_ = conn.Conn().Close(context.Background())
		conn.Release()
		authError(c, 503, "DB_ERROR")
		return nil, nil, false
	}
	release := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		unlocked, unlockErr := store.New(conn).PlatformReleaseDraftSessionLock(ctx, store.PlatformReleaseDraftSessionLockParams(args))
		if unlockErr != nil || !unlocked {
			_ = conn.Conn().Close(ctx)
		}
		conn.Release()
	}
	return conn, release, true
}

func beginDraftJournal(ctx context.Context, conn *pgxpool.Conn, session webmail.Session) (pgx.Tx, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	err = store.New(tx).PlatformSetDraftSaveScope(ctx, store.PlatformSetDraftSaveScopeParams{TenantID: strconv.FormatInt(session.TenantID, 10), MailboxID: strconv.FormatInt(session.MailboxID, 10)})
	if err != nil {
		_ = tx.Rollback(context.Background())
		return nil, err
	}
	return tx, nil
}

func (h *MailboxNativeHandler) saveDraftJournal(c *gin.Context, client webmail.Client, session webmail.Session, req mailboxDraftRequest, out webmail.Outgoing) (webmail.DraftSaveResult, bool) {
	key := c.GetHeader("Idempotency-Key")
	if !validScheduleKey(key) {
		authError(c, 400, "INVALID_PARAMS")
		return webmail.DraftSaveResult{}, false
	}
	appender, ok := client.(webmail.DraftAppender)
	if !ok {
		authError(c, 503, "IMAP_UNAVAILABLE")
		return webmail.DraftSaveResult{}, false
	}
	folder, err := client.EnsureDraftsFolder(c.Request.Context())
	if err != nil {
		authError(c, 502, "IMAP_UNAVAILABLE")
		return webmail.DraftSaveResult{}, false
	}
	conn, release, ok := h.acquireDraftJournal(c, session, folder, req.UID, req.UIDValidity)
	if !ok {
		return webmail.DraftSaveResult{}, false
	}
	defer release()
	tx, err := beginDraftJournal(c.Request.Context(), conn, session)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return webmail.DraftSaveResult{}, false
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback(context.Background())
		}
	}()
	q := store.New(tx)
	if req.UID != 0 && h.draftSubmissionLocked(c, folder, req.UID, req.UIDValidity, q) {
		return webmail.DraftSaveResult{}, false
	}
	ctx := c.Request.Context()
	body, _ := json.Marshal(req)
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	row, err := q.PlatformGetDataImportReceipt(ctx, store.PlatformGetDataImportReceiptParams{TenantID: session.TenantID, MailboxID: session.MailboxID, Kind: "draft_save", IdempotencyKey: key})
	var receipt draftSaveReceipt
	if err == nil {
		if json.Unmarshal(row.Payload, &receipt) != nil {
			authError(c, 503, "DB_ERROR")
			return webmail.DraftSaveResult{}, false
		}
		if receipt.Digest != digest {
			authError(c, 409, "IDEMPOTENCY_CONFLICT")
			return webmail.DraftSaveResult{}, false
		}
		if row.Status == "done" && !receipt.Result.CleanupPending {
			return receipt.Result, true
		}
		result, found, findErr := appender.FindDraftCopy(ctx, folder, receipt.MessageID, receipt.RawDigest)
		if findErr != nil || !found {
			authError(c, 409, "DRAFT_SAVE_UNCERTAIN")
			return result, false
		}
		receipt.Result = result
	} else {
		if !errors.Is(err, pgx.ErrNoRows) {
			authError(c, 503, "DB_ERROR")
			return webmail.DraftSaveResult{}, false
		}
		if req.UID != 0 {
			pending, pendingErr := q.PlatformDraftReplacementPending(ctx, store.PlatformDraftReplacementPendingParams{TenantID: session.TenantID, MailboxID: session.MailboxID, PreviousUid: strconv.FormatUint(uint64(req.UID), 10), PreviousValidity: strconv.FormatUint(uint64(req.UIDValidity), 10)})
			if pendingErr != nil {
				authError(c, 503, "DB_ERROR")
				return webmail.DraftSaveResult{}, false
			}
			if pending {
				authError(c, 409, "DRAFT_SAVE_UNCERTAIN")
				return webmail.DraftSaveResult{}, false
			}
			accessor, hasRaw := client.(webmail.RawMessageAccessor)
			if !hasRaw {
				authError(c, 503, "IMAP_UNAVAILABLE")
				return webmail.DraftSaveResult{}, false
			}
			_, validity, readErr := accessor.GetRawMessage(ctx, folder, req.UID)
			if readErr != nil || validity != req.UIDValidity {
				authError(c, 409, "DRAFT_CHANGED")
				return webmail.DraftSaveResult{}, false
			}
		}
		random := make([]byte, 16)
		if _, err = rand.Read(random); err != nil {
			authError(c, 503, "INTERNAL_ERROR")
			return webmail.DraftSaveResult{}, false
		}
		out.MessageID = fmt.Sprintf("<draft.%x@urmail.invalid>", random)
		raw := []byte(webmail.BuildDraftMIME(out.From, out))
		rawSum := sha256.Sum256(raw)
		receipt = draftSaveReceipt{Digest: digest, MessageID: out.MessageID, RawDigest: hex.EncodeToString(rawSum[:]), PreviousUID: req.UID, PreviousValidity: req.UIDValidity}
		data, _ := json.Marshal(receipt)
		// Commit intent on the same connection before APPEND. The session lock
		// remains held, so no second pool connection or lock gap is needed.
		err = q.PlatformClaimDraftSave(ctx, store.PlatformClaimDraftSaveParams{TenantID: session.TenantID, MailboxID: session.MailboxID, IdempotencyKey: key, Payload: data})
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			authError(c, 503, "DB_ERROR")
			return webmail.DraftSaveResult{}, false
		}
		tx, err = beginDraftJournal(ctx, conn, session)
		if err != nil {
			authError(c, 409, "DRAFT_SAVE_UNCERTAIN")
			return webmail.DraftSaveResult{}, false
		}
		q = store.New(tx)

		result, appendErr := appender.AppendPreparedDraft(ctx, folder, raw)
		if appendErr != nil {
			authError(c, 409, "DRAFT_SAVE_UNCERTAIN")
			return result, false
		}
		receipt.Result = result
	}
	if receipt.PreviousUID != 0 {
		if receipt.Result.UIDValidity != receipt.PreviousValidity {
			authError(c, 409, "DRAFT_SAVE_UNCERTAIN")
			return receipt.Result, false
		}
		receipt.Result.CleanupPending = client.DeleteMessages(ctx, folder, []uint32{receipt.PreviousUID}, true) != nil
	}
	// Finish independently of the response lifetime after APPEND is known.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	payload, _ := json.Marshal(receipt)
	count, completeErr := q.PlatformCompleteDraftSave(finish, store.PlatformCompleteDraftSaveParams{TenantID: session.TenantID, MailboxID: session.MailboxID, IdempotencyKey: key, Payload: payload})
	if completeErr != nil || count != 1 || tx.Commit(finish) != nil {
		authError(c, http.StatusConflict, "DRAFT_SAVE_UNCERTAIN")
		return receipt.Result, false
	}
	return receipt.Result, true
}
