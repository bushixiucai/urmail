package handlers

// Slice-engine webmail write path (pm-webmail-writepath-1): send pipeline,
// server-side drafts and send receipts served from the SQLite send ledger
// when the process carries no PG pool. The PG engine keeps its existing
// code paths untouched; every branch here answers the SAME wire contract
// (receipt JSON, i18n codes, fail-closed guards) as the PG engine.

import (
	"context"
	cryptoRand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"database/sql"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/mailboxsend"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// SendLedger is the slice-runtime persistence boundary for the webmail send
// pipeline, server-side drafts and send receipts.
type SendLedger interface {
	PrepareSend(ctx context.Context, arg store.PlatformCreateSendReceiptParams, digest string) (store.MailboxSendReceipts, bool, error)
	BeginSubmit(ctx context.Context, tenant, mailbox, id int64) (store.MailboxSendReceipts, error)
	FinishSubmit(ctx context.Context, tenant, mailbox, id, claimToken int64, status, code string) (store.MailboxSendReceipts, error)
	GetByKey(ctx context.Context, tenant, mailbox int64, key string) (store.MailboxSendReceipts, error)
	Get(ctx context.Context, tenant, mailbox, id int64) (store.MailboxSendReceipts, error)
	List(ctx context.Context, tenant, mailbox, limit, offset int64) ([]store.MailboxSendReceipts, error)
	ClaimOwnedCopy(ctx context.Context, tenant, mailbox, id int64) (store.MailboxSendReceipts, error)
	DeferCopy(ctx context.Context, tenant, mailbox, id, claimToken int64) error
	MarkCopyUnknown(ctx context.Context, tenant, mailbox, id, claimToken int64) error
	CompleteCopy(ctx context.Context, tenant, mailbox, id, claimToken int64) error
	ClaimDraftSave(ctx context.Context, tenant, mailbox int64, key, digest, messageID string) (status string, resultJSON string, err error)
	CompleteDraftSave(ctx context.Context, tenant, mailbox int64, key, resultJSON, folder string, uid, uidvalidity int64) (int64, error)
}

// --- send pipeline ---

// sendMessageSlice is the ledger-backed send flow. Semantics mirror the PG
// engine: idempotency key + canonical digest decide replay vs conflict; a
// source draft holds one live submission; SMTP is crossed exactly once under
// a claim lease (a lost lease resolves to `uncertain`, never an automatic
// retry); the Sent copy reconciles inline and degrades to `pending`.
func (h *MailboxNativeHandler) sendMessageSlice(c *gin.Context) {
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if !validScheduleKey(key) {
		authError(c, 400, "SEND_IDEMPOTENCY_REQUIRED")
		return
	}
	var req mailboxSendRequest
	if !bindMailboxMessage(c, &req) {
		return
	}
	if !utf8.ValidString(req.Subject) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	canonical, _ := json.Marshal(req)
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	ledger := h.SendLedger
	if prior, err := ledger.GetByKey(c.Request.Context(), session.TenantID, session.MailboxID, key); err == nil {
		if prior.RequestHash != digest {
			authError(c, 409, "IDEMPOTENCY_CONFLICT")
			return
		}
		returnSendReceipt(c, prior)
		return
	} else if !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, sql.ErrNoRows) {
		authError(c, 503, "DB_ERROR")
		return
	}
	_, from, ok := h.authorizedIdentity(c, session, req.From)
	if !ok {
		return
	}
	to, cc, bcc := cleanAddrs(req.To), cleanAddrs(req.Cc), cleanAddrs(req.Bcc)
	if len(to)+len(cc)+len(bcc) == 0 || len(req.Subject) > 998 || !utf8.ValidString(req.Subject) || !validReplyHeader(req.InReplyTo) || len(req.Attachments) > maxSendAttachments {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	atts, ok := decodeSendAttachments(c, req.Attachments)
	if !ok {
		return
	}
	if h.MaySend != nil {
		allowed, e := h.MaySend(c.Request.Context(), session.TenantID, session.MailboxID)
		if e != nil {
			authError(c, 502, "MAIL_POLICY_UNAVAILABLE")
			return
		}
		if !allowed {
			authError(c, 403, "MAIL_SENDING_SUSPENDED")
			return
		}
	}
	password, ok := h.vaultPassword(c)
	if !ok {
		return
	}
	ref := mailboxsend.DraftRef{}
	if req.DraftSource != nil {
		folder, e := webmail.DecodeFolderName(req.DraftSource.Folder)
		if e != nil || req.DraftSource.UID == 0 || req.DraftSource.UIDValidity == 0 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		ref = mailboxsend.DraftRef{Folder: folder, UID: req.DraftSource.UID, Validity: req.DraftSource.UIDValidity}
		client, _, ok := h.dialIMAP(c)
		if !ok {
			return
		}
		drafts, e := client.EnsureDraftsFolder(c.Request.Context())
		if e != nil || drafts != folder {
			_ = client.Close()
			authError(c, 409, "DRAFT_CHANGED")
			return
		}
		validity, e := client.FolderUIDValidity(c.Request.Context(), folder)
		if e == nil && validity == ref.Validity {
			_, e = client.GetMessage(c.Request.Context(), folder, ref.UID)
		} else if e == nil {
			e = webmail.ErrDraftChanged
		}
		_ = client.Close()
		if e != nil {
			authError(c, 409, "DRAFT_CHANGED")
			return
		}
	}
	out := webmail.FreezeOutgoing(webmail.Outgoing{From: from, InReplyTo: req.InReplyTo, References: req.InReplyTo, To: to, Cc: cc, Bcc: bcc, Subject: req.Subject, Text: req.Text, HTML: req.HTML, Attachments: atts})
	out.Raw = []byte(webmail.BuildMIME(from, out))
	rawSealed, err := mailboxsend.Seal(h.Secrets, string(out.Raw))
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	smtpSealed, err := mailboxsend.Seal(h.Secrets, password)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	row, created, err := ledger.PrepareSend(c.Request.Context(), store.PlatformCreateSendReceiptParams{
		TenantID: session.TenantID, MailboxID: session.MailboxID, RequestKey: key, RequestHash: digest,
		MessageID: out.MessageID, RawSecret: rawSealed, SmtpSecret: smtpSealed,
		DraftFolder: ref.Folder, DraftUid: int64(ref.UID), DraftUidvalidity: int64(ref.Validity),
	}, digest)
	if errors.Is(err, ErrLedgerConflict) {
		authError(c, 409, "IDEMPOTENCY_CONFLICT")
		return
	}
	if errors.Is(err, ErrLedgerDraftLocked) {
		authError(c, 409, "DRAFT_SEND_LOCKED")
		return
	}
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if !created {
		returnSendReceipt(c, row)
		return
	}
	// Cross the SMTP boundary exactly once under the claim lease. An HTTP
	// disconnect cannot interrupt the final-state write.
	claimed, err := ledger.BeginSubmit(c.Request.Context(), session.TenantID, session.MailboxID, row.ID)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	attempt, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 90*time.Second)
	defer cancel()
	_, sendErr := h.Submitter.Send(attempt, session.Address, password, out)
	status, code := ledgerAccepted, ""
	if sendErr != nil {
		status = ledgerUncertain
		code = "MAILBOX_SEND_UNCERTAIN"
		if errors.Is(sendErr, webmail.ErrSubmitFailed) {
			status = ledgerRejected
			code = "SMTP_UNAVAILABLE"
		}
	}
	final, finishErr := ledger.FinishSubmit(c.Request.Context(), session.TenantID, session.MailboxID, row.ID, claimed.ClaimToken, status, code)
	if finishErr != nil {
		// The lease will resolve the committed submitting row to
		// `uncertain`; surface the same uncertainty now.
		authError(c, 502, "MAILBOX_SEND_UNCERTAIN")
		return
	}
	// Side effects after SMTP never use helpers which write an HTTP error.
	if final.Status == ledgerAccepted && h.Dialer != nil {
		copyCtx, copyCancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 90*time.Second)
		defer copyCancel()
		if claim, e := ledger.ClaimOwnedCopy(copyCtx, final.TenantID, final.MailboxID, final.ID); e == nil {
			if client, e := h.Dialer.Dial(copyCtx, session.Address, password); e == nil {
				h.reconcileSentCopySlice(copyCtx, ledger, claim, client, session)
				_ = client.Close()
			} else if deferErr := ledger.DeferCopy(copyCtx, final.TenantID, final.MailboxID, final.ID, claim.ClaimToken); deferErr != nil {
				final.SentCopyState = "pending"
			}
		}
		final, _ = ledger.Get(copyCtx, final.TenantID, final.MailboxID, final.ID)
	}
	if final.Status != ledgerAccepted {
		code := "SMTP_UNAVAILABLE"
		if final.Status == ledgerUncertain {
			code = "MAILBOX_SEND_UNCERTAIN"
		}
		authError(c, http.StatusBadGateway, code)
		return
	}
	returnSendReceipt(c, final)
}

// reconcileSentCopySlice mirrors mailboxsend.Service.Repair: verify the exact
// bytes live in Sent, APPEND when they do not, then close the copy state and
// release the source draft. Every failure degrades to `pending` (never an
// HTTP error — the response is already committed by the caller).
func (h *MailboxNativeHandler) reconcileSentCopySlice(ctx context.Context, ledger SendLedger, claim store.MailboxSendReceipts, client webmail.Client, session webmail.Session) {
	deferPending := func(cause error) {
		println("ZZ defer:", cause.Error())
		_ = ledger.DeferCopy(ctx, claim.TenantID, claim.MailboxID, claim.ID, claim.ClaimToken)
	}
	if h.Secrets == nil || !strings.HasPrefix(claim.RawSecret, "enc:v1:") {
		deferPending(errors.New("copy recovery unavailable"))
		return
	}
	raw, err := h.Secrets.DecryptString(claim.RawSecret)
	if err != nil {
		deferPending(err)
		return
	}
	finder, ok := client.(webmail.SentCopyFinder)
	if !ok {
		deferPending(errors.New("copy reconciliation unavailable"))
		return
	}
	authorize := func() error {
		if session.TenantID != claim.TenantID || session.MailboxID != claim.MailboxID {
			return errors.New("copy owner mismatch")
		}
		return nil
	}
	if err = authorize(); err != nil {
		deferPending(err)
		return
	}
	_, found, err := finder.FindSentCopy(ctx, []byte(raw))
	if err != nil {
		deferPending(err)
		return
	}
	if err = authorize(); err != nil {
		deferPending(err)
		return
	}
	if !found {
		if err = ledger.MarkCopyUnknown(ctx, claim.TenantID, claim.MailboxID, claim.ID, claim.ClaimToken); err != nil {
			deferPending(err)
			return
		}
		if _, err = client.SaveSentCopy(ctx, []byte(raw)); err != nil {
			deferPending(err)
			return
		}
	}
	if err = authorize(); err != nil {
		deferPending(err)
		return
	}
	if err = ledger.CompleteCopy(ctx, claim.TenantID, claim.MailboxID, claim.ID, claim.ClaimToken); err != nil {
		deferPending(err)
		return
	}
	if claim.DraftUid > 0 && claim.DraftUid <= math.MaxUint32 {
		validity, e := client.FolderUIDValidity(ctx, claim.DraftFolder)
		if e == nil && int64(validity) == claim.DraftUidvalidity {
			_ = client.DeleteMessages(ctx, claim.DraftFolder, []uint32{uint32(claim.DraftUid)}, true)
		}
	}
}

// --- server-side drafts ---

// saveDraftJournalSlice mirrors the PG draft journal semantics on the ledger:
// claim the idempotency key (conflict on digest mismatch, conservative 409 on
// an unfinished claim), APPEND via the IMAP DraftAppender, then complete.
func (h *MailboxNativeHandler) saveDraftJournalSlice(c *gin.Context, appender webmail.DraftAppender, client webmail.Client, session webmail.Session, req mailboxDraftRequest, out webmail.Outgoing, folder string) (webmail.DraftSaveResult, bool) {
	ledger := h.SendLedger
	key := c.GetHeader("Idempotency-Key")
	if !validScheduleKey(key) {
		authError(c, 400, "INVALID_PARAMS")
		return webmail.DraftSaveResult{}, false
	}
	body, _ := json.Marshal(req)
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	random := make([]byte, 16)
	if _, err := cryptoRand.Read(random); err != nil {
		authError(c, 503, "INTERNAL_ERROR")
		return webmail.DraftSaveResult{}, false
	}
	out.MessageID = "<draft." + hex.EncodeToString(random) + "@urmail.invalid>"
	raw := []byte(webmail.BuildDraftMIME(out.From, out))
	rawSum := sha256.Sum256(raw)
	receipt := draftSaveReceipt{Digest: digest, MessageID: out.MessageID, RawDigest: hex.EncodeToString(rawSum[:]), PreviousUID: req.UID, PreviousValidity: req.UIDValidity}

	status, stored, err := ledger.ClaimDraftSave(c.Request.Context(), session.TenantID, session.MailboxID, key, digest, out.MessageID)
	if errors.Is(err, ErrLedgerConflict) {
		authError(c, 409, "IDEMPOTENCY_CONFLICT")
		return webmail.DraftSaveResult{}, false
	}
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return webmail.DraftSaveResult{}, false
	}
	switch status {
	case "done":
		var prior draftSaveReceipt
		if jsonErr := json.Unmarshal([]byte(stored), &prior); jsonErr != nil {
			authError(c, 503, "DB_ERROR")
			return webmail.DraftSaveResult{}, false
		}
		if !prior.Result.CleanupPending {
			return prior.Result, true
		}
		result, found, findErr := appender.FindDraftCopy(c.Request.Context(), folder, prior.MessageID, prior.RawDigest)
		if findErr != nil || !found {
			authError(c, 409, "DRAFT_SAVE_UNCERTAIN")
			return result, false
		}
		return result, true
	case "claimed":
		// Fresh insert (this call claimed the key): APPEND and complete.
		return h.finishFreshDraftSaveSlice(c, appender, client, session, key, &receipt, out, folder)
	default:
		// claimed by a concurrent request and still unfinished: conservative
		// fail-closed (a second append here could duplicate the draft).
		authError(c, 409, "DRAFT_SAVE_UNCERTAIN")
		return webmail.DraftSaveResult{}, false
	}
}

// finishFreshDraftSaveSlice appends the fresh draft and completes the claim.
func (h *MailboxNativeHandler) finishFreshDraftSaveSlice(c *gin.Context, appender webmail.DraftAppender, client webmail.Client, session webmail.Session, key string, receipt *draftSaveReceipt, out webmail.Outgoing, folder string) (webmail.DraftSaveResult, bool) {
	result, appendErr := appender.AppendPreparedDraft(c.Request.Context(), folder, out.Raw)
	if appendErr != nil {
		authError(c, 409, "DRAFT_SAVE_UNCERTAIN")
		return result, false
	}
	receipt.Result = result
	if receipt.PreviousUID != 0 {
		if receipt.Result.UIDValidity != receipt.PreviousValidity {
			authError(c, 409, "DRAFT_SAVE_UNCERTAIN")
			return receipt.Result, false
		}
		receipt.Result.CleanupPending = client.DeleteMessages(c.Request.Context(), folder, []uint32{receipt.PreviousUID}, true) != nil
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
	defer cancel()
	payload, _ := json.Marshal(receipt)
	count, err := h.SendLedger.CompleteDraftSave(finish, session.TenantID, session.MailboxID, key, string(payload), receipt.Result.Folder, int64(receipt.Result.UID), int64(receipt.Result.UIDValidity))
	if err != nil || count != 1 {
		authError(c, http.StatusConflict, "DRAFT_SAVE_UNCERTAIN")
		return receipt.Result, false
	}
	return receipt.Result, true
}

// completeDraftSaveSlice finishes the claimed save after a successful APPEND

// --- send receipts ---

// listSendReceiptsSlice serves the owner-scoped receipt list from the ledger.
func (h *MailboxNativeHandler) listSendReceiptsSlice(c *gin.Context, session webmail.Session) {
	limit, _ := strconv.ParseInt(c.Query("limit"), 10, 32)
	offset, _ := strconv.ParseInt(c.Query("offset"), 10, 32)
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	items, err := h.SendLedger.List(c.Request.Context(), session.TenantID, session.MailboxID, limit, offset)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, row := range items {
		out = append(out, receiptJSON(row))
	}
	c.JSON(http.StatusOK, gin.H{"receipts": out})
}

// getSendReceiptSlice serves one owner-scoped receipt from the ledger.
func (h *MailboxNativeHandler) getSendReceiptSlice(c *gin.Context, session webmail.Session) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	row, err := h.SendLedger.Get(c.Request.Context(), session.TenantID, session.MailboxID, id)
	if errors.Is(err, sql.ErrNoRows) {
		authError(c, 404, "NOT_FOUND")
		return
	}
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, receiptJSON(row))
}

// repairSendReceiptCopySlice mirrors mailboxsend.Service.Repair through the
// ledger: reconcile the exact bytes in Sent, APPEND when missing, then close
// the copy state under the owner claim.
func (h *MailboxNativeHandler) repairSendReceiptCopySlice(c *gin.Context, session webmail.Session) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	ledger := h.SendLedger
	claim, err := ledger.ClaimOwnedCopy(c.Request.Context(), session.TenantID, session.MailboxID, id)
	if errors.Is(err, sql.ErrNoRows) {
		authError(c, 404, "NOT_FOUND")
		return
	}
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	if h.Secrets == nil || !strings.HasPrefix(claim.RawSecret, "enc:v1:") {
		_ = ledger.DeferCopy(c.Request.Context(), claim.TenantID, claim.MailboxID, claim.ID, claim.ClaimToken)
		authError(c, 409, "SENT_COPY_PENDING")
		return
	}
	raw, err := h.Secrets.DecryptString(claim.RawSecret)
	if err != nil {
		_ = ledger.DeferCopy(c.Request.Context(), claim.TenantID, claim.MailboxID, claim.ID, claim.ClaimToken)
		authError(c, 409, "SENT_COPY_PENDING")
		return
	}
	finder, ok := client.(webmail.SentCopyFinder)
	if !ok {
		_ = ledger.DeferCopy(c.Request.Context(), claim.TenantID, claim.MailboxID, claim.ID, claim.ClaimToken)
		authError(c, 502, "IMAP_UNAVAILABLE")
		return
	}
	_, found, err := finder.FindSentCopy(c.Request.Context(), []byte(raw))
	if err == nil && !found {
		if markErr := ledger.MarkCopyUnknown(c.Request.Context(), claim.TenantID, claim.MailboxID, claim.ID, claim.ClaimToken); markErr == nil {
			_, err = client.SaveSentCopy(c.Request.Context(), []byte(raw))
		}
	}
	if err != nil {
		_ = ledger.DeferCopy(c.Request.Context(), claim.TenantID, claim.MailboxID, claim.ID, claim.ClaimToken)
		authError(c, 409, "SENT_COPY_PENDING")
		return
	}
	if err = ledger.CompleteCopy(c.Request.Context(), claim.TenantID, claim.MailboxID, claim.ID, claim.ClaimToken); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if claim.DraftUid > 0 && claim.DraftUid <= math.MaxUint32 {
		if validity, e := client.FolderUIDValidity(c.Request.Context(), claim.DraftFolder); e == nil && int64(validity) == claim.DraftUidvalidity {
			_ = client.DeleteMessages(c.Request.Context(), claim.DraftFolder, []uint32{uint32(claim.DraftUid)}, true)
		}
	}
	row, err := ledger.Get(c.Request.Context(), claim.TenantID, claim.MailboxID, id)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"receipt": receiptJSON(row), "sent_copy": "saved"})
}
