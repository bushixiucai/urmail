package handlers

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// Draft API (S2 / W2) — IMAP \Drafts only. Scheduled send metadata is T2
// (/mailbox/scheduled-sends); bodies remain in IMAP.
//
//	PUT  /api/v1/mailbox/drafts           save (optional uid = replace via append+delete)
//	GET  /api/v1/mailbox/drafts           list summaries in the resolved Drafts folder
//	DELETE /api/v1/mailbox/drafts/:uid    expunge one draft UID
//
// Folder resolution: SPECIAL-USE \Drafts, else name "Drafts", else CREATE "Drafts".
// Message bodies reuse BuildMIME (same attachment fields as POST /mailbox/send).
// Clients may also list/open Drafts via /folders/{name_enc}/messages when known.

type mailboxDraftRequest struct {
	From        string                  `json:"from"`
	InReplyTo   string                  `json:"in_reply_to"`
	ReplySource *mailboxReplySource     `json:"reply_source"`
	UID         uint32                  `json:"uid"` // prior draft to replace; 0 = new
	UIDValidity uint32                  `json:"uid_validity"`
	To          []string                `json:"to"`
	Cc          []string                `json:"cc"`
	Bcc         []string                `json:"bcc"`
	Subject     string                  `json:"subject"`
	Text        string                  `json:"text"`
	HTML        string                  `json:"html"`
	Attachments []mailboxSendAttachment `json:"attachments"`
}

// SaveDraft APPENDs (and optionally replaces) a draft under mailbox auth.
func (h *MailboxNativeHandler) SaveDraft(c *gin.Context) {
	client, session, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()

	var req mailboxDraftRequest
	if !bindMailboxMessage(c, &req) {
		return
	}
	if len(req.Subject) > 998 || !utf8.ValidString(req.Subject) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if len(req.Attachments) > maxSendAttachments {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	atts, ok := decodeSendAttachments(c, req.Attachments)
	if !ok {
		return
	}
	_, from, ok := h.authorizedIdentity(c, session, req.From)
	if !ok {
		return
	}
	inReplyTo, ok := resolveReplySource(c, client, req.ReplySource, req.InReplyTo)
	if !ok {
		return
	}
	result, saved := h.saveDraftJournal(c, client, session, req, webmail.Outgoing{
		From: from, InReplyTo: inReplyTo, References: inReplyTo,
		To: cleanAddrs(req.To), Cc: cleanAddrs(req.Cc), Bcc: cleanAddrs(req.Bcc),
		Subject: req.Subject, Text: req.Text, HTML: req.HTML, Attachments: atts,
	})
	if !saved {
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"folder":          result.Folder,
		"name_enc":        result.NameEnc,
		"uid":             result.UID,
		"uid_validity":    result.UIDValidity,
		"cleanup_pending": result.CleanupPending,
		"saved":           true,
	})
}

// ListDrafts returns a page of messages from the resolved Drafts folder.
func (h *MailboxNativeHandler) ListDrafts(c *gin.Context) {
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
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	opts := webmail.ClampListOpts(webmail.ListOpts{Limit: limit, Offset: offset})
	result, err := client.ListMessages(c.Request.Context(), folder, opts)
	if err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	if result.Messages == nil {
		result.Messages = []webmail.MessageSummary{}
	}
	c.JSON(http.StatusOK, gin.H{
		"folder":   folder,
		"name_enc": webmail.EncodeFolderName(folder),
		"messages": result.Messages,
		"total":    result.Total,
		"limit":    opts.Limit,
		"offset":   opts.Offset,
	})
}

// DeleteDraft expunges one UID from the drafts folder.
func (h *MailboxNativeHandler) DeleteDraft(c *gin.Context) {
	uid, err := strconv.ParseUint(c.Param("uid"), 10, 32)
	if err != nil || uid == 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	validity, err := strconv.ParseUint(c.Query("uid_validity"), 10, 32)
	if err != nil || validity == 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	client, session, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()

	folder, err := client.EnsureDraftsFolder(c.Request.Context())
	if err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	tx, locked := h.lockDraftEdit(c, session, folder, uint32(uid), uint32(validity))
	if !locked {
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if h.draftDeletionLocked(c, folder, uint32(uid), uint32(validity), store.New(tx)) {
		return
	}
	pending, pendingErr := store.New(tx).PlatformDraftReplacementPending(c.Request.Context(), store.PlatformDraftReplacementPendingParams{TenantID: session.TenantID, MailboxID: session.MailboxID, PreviousUid: strconv.FormatUint(uid, 10), PreviousValidity: strconv.FormatUint(validity, 10)})
	if pendingErr != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if pending {
		authError(c, 409, "DRAFT_SAVE_UNCERTAIN")
		return
	}
	currentValidity, err := client.FolderUIDValidity(c.Request.Context(), folder)
	if err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	if uint32(validity) != currentValidity {
		authError(c, http.StatusConflict, "DRAFT_CHANGED")
		return
	}
	if err = client.DeleteMessages(c.Request.Context(), folder, []uint32{uint32(uid)}, true); err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"deleted":  true,
		"uid":      uint32(uid),
		"folder":   folder,
		"name_enc": webmail.EncodeFolderName(folder),
	})
}

func decodeSendAttachments(c *gin.Context, in []mailboxSendAttachment) ([]webmail.OutgoingAttachment, bool) {
	var atts []webmail.OutgoingAttachment
	totalBytes := 0
	for _, a := range in {
		if len(a.ContentID) > 998 || strings.ContainsFunc(a.ContentID, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) || r == '<' || r == '>' }) {
			authError(c, 400, "INVALID_PARAMS")
			return nil, false
		}
		raw, err := base64.StdEncoding.DecodeString(a.ContentB64)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(a.ContentB64)
		}
		if err != nil || len(raw) == 0 {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
			return nil, false
		}
		totalBytes += len(raw)
		if len(raw) > maxSendAttachmentBytes || totalBytes > maxSendTotalAttachmentBytes {
			authError(c, http.StatusRequestEntityTooLarge, "MAILBOX_MESSAGE_TOO_LARGE")
			return nil, false
		}
		atts = append(atts, webmail.OutgoingAttachment{
			ContentID: a.ContentID, Inline: a.Inline,
			Filename:    strings.TrimSpace(a.Filename),
			ContentType: strings.TrimSpace(a.ContentType),
			Content:     raw,
		})
	}
	return atts, true
}
