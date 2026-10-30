package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/mailboxsend"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type mailboxSendAttachment struct {
	ContentID   string `json:"content_id,omitempty"`
	Inline      bool   `json:"inline,omitempty"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	ContentB64  string `json:"content_base64"`
}

type mailboxSendRequest struct {
	DraftSource                    *mailboxReplySource     `json:"draft_source"`
	AcknowledgedUncertainReceiptID int64                   `json:"acknowledged_uncertain_receipt_id"`
	InReplyTo                      string                  `json:"in_reply_to"`
	From                           string                  `json:"from"`
	To                             []string                `json:"to"`
	Cc                             []string                `json:"cc"`
	Bcc                            []string                `json:"bcc"`
	Subject                        string                  `json:"subject"`
	Text                           string                  `json:"text"`
	HTML                           string                  `json:"html"`
	Attachments                    []mailboxSendAttachment `json:"attachments"`
}

const maxSendAttachmentBytes = 5 << 20
const maxSendTotalAttachmentBytes = 10 << 20
const maxSendAttachments = 10

// SendMessage submits mail via local SMTP AUTH using the vaulted password.
func (h *MailboxNativeHandler) SendMessage(c *gin.Context) {
	if !h.requireEnabled(c) {
		return
	}
	if h.Submitter == nil || h.Vault == nil || h.Pool == nil || h.Secrets == nil {
		authError(c, 503, "WEBMAIL_DISABLED")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
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
	canonical, _ := json.Marshal(req)
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	q := store.New(h.Pool.Pool)
	prior, err := q.PlatformGetSendReceiptByKey(c.Request.Context(), store.PlatformGetSendReceiptByKeyParams{TenantID: session.TenantID, MailboxID: session.MailboxID, RequestKey: key})
	if err == nil {
		if prior.RequestHash != digest {
			authError(c, 409, "IDEMPOTENCY_CONFLICT")
			return
		}
		returnSendReceipt(c, prior)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
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
	service := h.sendReceiptService()
	service.ValidateDraft = func(ctx context.Context, ref mailboxsend.DraftRef) error {
		client, e := h.Dialer.Dial(ctx, session.Address, password)
		if e != nil {
			return e
		}
		defer func() { _ = client.Close() }()
		drafts, e := client.EnsureDraftsFolder(ctx)
		if e != nil {
			return e
		}
		if drafts != ref.Folder {
			return webmail.ErrDraftChanged
		}
		validity, e := client.FolderUIDValidity(ctx, ref.Folder)
		if e != nil {
			return e
		}
		if validity != ref.Validity {
			return webmail.ErrDraftChanged
		}
		_, e = client.GetMessage(ctx, ref.Folder, ref.UID)
		if e != nil && (errors.Is(e, webmail.ErrMessageNotFound) || strings.Contains(e.Error(), "not found")) {
			return webmail.ErrDraftChanged
		}
		return e
	}
	row, created, err := service.Prepare(c.Request.Context(), session.TenantID, session.MailboxID, key, digest, password, out, ref, req.AcknowledgedUncertainReceiptID)
	if errors.Is(err, mailboxsend.ErrConflict) {
		authError(c, 409, "IDEMPOTENCY_CONFLICT")
		return
	}
	if errors.Is(err, webmail.ErrDraftChanged) {
		authError(c, 409, "DRAFT_CHANGED")
		return
	}
	if errors.Is(err, mailboxsend.ErrDraftLocked) {
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
	row, err = service.Submit(c.Request.Context(), row, session.Address, password, out)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	// Side effects after SMTP never use helpers which write an HTTP error.
	if row.Status == "accepted" && h.Dialer != nil {
		copyCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 90*time.Second)
		if claim, e := service.ClaimOwner(copyCtx, row.TenantID, row.MailboxID, row.ID); e == nil {
			if client, e := h.Dialer.Dial(copyCtx, session.Address, password); e == nil {
				_ = service.Repair(copyCtx, claim, client, h.copySessionAuthority(session))
				_ = client.Close()
			} else {
				_, _ = q.PlatformDeferSendCopy(copyCtx, store.PlatformDeferSendCopyParams{TenantID: row.TenantID, MailboxID: row.MailboxID, ID: row.ID, ClaimToken: claim.ClaimToken})
			}
		}
		if current, e := q.PlatformGetSendReceipt(copyCtx, store.PlatformGetSendReceiptParams{TenantID: row.TenantID, MailboxID: row.MailboxID, ID: row.ID}); e == nil {
			row = current
		}
		cancel()
	}
	returnSendReceipt(c, row)
}

func cleanAddrs(in []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, a := range in {
		a = strings.TrimSpace(strings.ToLower(a))
		if a == "" || len(a) > 320 || !strings.Contains(a, "@") {
			continue
		}
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	return out
}

// bindMailboxMessage keeps transport overflow distinct from malformed JSON.
func bindMailboxMessage(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			authError(c, http.StatusRequestEntityTooLarge, "MAILBOX_MESSAGE_TOO_LARGE")
		} else {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		}
		return false
	}
	return true
}

// MailAllowed adapts a mail-service allowlist lookup into the send guard
// signature; it returns nil when no allowlist is configured.
func MailAllowed(q interface {
	MailServiceAllowed(context.Context, int64) (bool, error)
}) func(context.Context, int64, int64) (bool, error) {
	if q == nil {
		return nil
	}
	return func(ctx context.Context, tenantID, _ int64) (bool, error) { return q.MailServiceAllowed(ctx, tenantID) }
}

type mailboxReplySource struct {
	Folder      string `json:"folder_enc"`
	UID         uint32 `json:"uid"`
	UIDValidity uint32 `json:"uid_validity"`
}

func validReplyHeader(value string) bool {
	return len(value) <= 998 && !strings.ContainsAny(value, "\r\n\x00")
}
func resolveReplySource(c *gin.Context, client webmail.Client, source *mailboxReplySource, header string) (string, bool) {
	if !validReplyHeader(header) {
		authError(c, 400, "INVALID_PARAMS")
		return "", false
	}
	if source == nil {
		return header, true
	}
	folder, err := webmail.DecodeFolderName(source.Folder)
	if err != nil || source.UID == 0 || source.UIDValidity == 0 {
		authError(c, 400, "INVALID_PARAMS")
		return "", false
	}
	validity, err := client.FolderUIDValidity(c.Request.Context(), folder)
	if err != nil || validity != source.UIDValidity {
		authError(c, 409, "AI_SOURCE_CHANGED")
		return "", false
	}
	original, err := client.GetMessage(c.Request.Context(), folder, source.UID)
	if err != nil || original == nil || original.UIDValidity != validity {
		authError(c, 409, "AI_SOURCE_CHANGED")
		return "", false
	}
	if !validReplyHeader(original.MessageID) {
		authError(c, 409, "AI_SOURCE_CHANGED")
		return "", false
	}
	return original.MessageID, true
}
