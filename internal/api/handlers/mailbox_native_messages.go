package handlers

import (
	"errors"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/webmail"
)

// ListMessages returns a page of UID summaries for a folder.
//
// Query params:
//   - limit, offset — pagination (clamped by webmail.ClampListOpts)
//   - q — optional search string; when set, IMAP UID SEARCH replaces ALL.
//     See webmail.BuildSearchCriteria for supported forms (subject:/from:/
//     text:/is:unseen and bare text → OR SUBJECT/FROM). Invalid q → 400.
func (h *MailboxNativeHandler) ListMessages(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	q := strings.TrimSpace(c.Query("q"))
	// Validate before dialing so bad q fails closed without an IMAP round-trip.
	if _, err := webmail.BuildSearchCriteria(q); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_SEARCH_QUERY")
		return
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()

	opts := webmail.ClampListOpts(webmail.ListOpts{Limit: limit, Offset: offset, Query: q})
	result, err := client.ListMessages(c.Request.Context(), folder, opts)
	if err != nil {
		if errors.Is(err, webmail.ErrInvalidSearchQuery) {
			authError(c, http.StatusBadRequest, "INVALID_SEARCH_QUERY")
			return
		}
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	if result.Messages == nil {
		result.Messages = []webmail.MessageSummary{}
	}
	c.JSON(http.StatusOK, gin.H{
		"messages": result.Messages,
		"total":    result.Total,
		"limit":    opts.Limit,
		"offset":   opts.Offset,
		"q":        q,
	})
}

// GetMessage returns body text/html and attachment metadata for one UID.
func (h *MailboxNativeHandler) GetMessage(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	uid, err := strconv.ParseUint(c.Param("uid"), 10, 32)
	if err != nil || uid == 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	msg, err := client.GetMessage(c.Request.Context(), folder, uint32(uid))
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			authError(c, http.StatusNotFound, "MESSAGE_NOT_FOUND")
			return
		}
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	if msg.Attachments == nil {
		msg.Attachments = []webmail.AttachmentMeta{}
	}
	c.JSON(http.StatusOK, msg)
}

// GetAttachment streams one MIME part as a downloadable attachment.
// GET /mailbox/folders/:name/messages/:uid/attachments/:part_id
func (h *MailboxNativeHandler) GetAttachment(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	uid, err := strconv.ParseUint(c.Param("uid"), 10, 32)
	if err != nil || uid == 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	partID := strings.TrimSpace(c.Param("part_id"))
	if partID == "" || len(partID) > 64 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	for _, r := range partID {
		if r < '0' || r > '9' {
			if r != '.' {
				authError(c, http.StatusBadRequest, "INVALID_PARAMS")
				return
			}
		}
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	filename, _, data, err := client.GetAttachment(c.Request.Context(), folder, uint32(uid), partID)
	if err != nil {
		if errors.Is(err, webmail.ErrAttachmentTooLarge) {
			authError(c, http.StatusRequestEntityTooLarge, "ATTACHMENT_TOO_LARGE")
			return
		}
		if errors.Is(err, webmail.ErrInvalidMIME) {
			authError(c, http.StatusUnprocessableEntity, "ATTACHMENT_INVALID")
			return
		}
		if strings.Contains(err.Error(), "not found") {
			authError(c, http.StatusNotFound, "ATTACHMENT_NOT_FOUND")
			return
		}
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	safeName := path.Base(strings.ReplaceAll(filename, "\\", "/"))
	if safeName == "." || safeName == "/" || safeName == "" {
		safeName = "attachment-" + partID
	}
	disp := mime.FormatMediaType("attachment", map[string]string{"filename": safeName})
	if disp == "" {
		disp = `attachment; filename="attachment-` + partID + `"`
	}
	c.Header("Content-Disposition", disp)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Header("Cache-Control", "no-store")
	// Always octet-stream + nosniff (same posture as support/archive downloads).
	c.Data(http.StatusOK, "application/octet-stream", data)
}

type mailboxFlagsRequest struct {
	UIDs   []uint32 `json:"uids"`
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

// FlagMessages adds/removes IMAP flags for UIDs in a folder.
// POST /mailbox/folders/:name/messages/flags
func (h *MailboxNativeHandler) FlagMessages(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	var req mailboxFlagsRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.UIDs) == 0 || (len(req.Add) == 0 && len(req.Remove) == 0) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if len(req.UIDs) > webmail.MaxPageSize {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	add, remove := sanitizeFlags(req.Add), sanitizeFlags(req.Remove)
	if len(add) == 0 && len(remove) == 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	if err := client.SetFlags(c.Request.Context(), folder, req.UIDs, add, remove); err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": len(req.UIDs)})
}

type mailboxMoveRequest struct {
	UIDs []uint32 `json:"uids"`
	To   string   `json:"to"`
}

// MoveMessages moves UIDs to another folder (IMAP UID MOVE).
// POST /mailbox/folders/:name/messages/move
func (h *MailboxNativeHandler) MoveMessages(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	var req mailboxMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.UIDs) == 0 || strings.TrimSpace(req.To) == "" {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if len(req.UIDs) > webmail.MaxPageSize {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	dest := strings.TrimSpace(req.To)
	if decoded, err := webmail.DecodeFolderName(dest); err == nil {
		dest = decoded
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	if err := client.MoveMessages(c.Request.Context(), folder, req.UIDs, dest); err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"moved": len(req.UIDs), "to": dest})
}

type mailboxDeleteRequest struct {
	UIDs    []uint32 `json:"uids"`
	Expunge bool     `json:"expunge"`
}

// DeleteMessages marks UIDs \Deleted and optionally expunges.
// POST /mailbox/folders/:name/messages/delete
func (h *MailboxNativeHandler) DeleteMessages(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	var req mailboxDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.UIDs) == 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if len(req.UIDs) > webmail.MaxPageSize {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	if err := client.DeleteMessages(c.Request.Context(), folder, req.UIDs, req.Expunge); err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": len(req.UIDs), "expunged": req.Expunge})
}

func sanitizeFlags(flags []string) []string {
	allowed := map[string]struct{}{
		`\Seen`: {}, `\Answered`: {}, `\Flagged`: {}, `\Deleted`: {}, `\Draft`: {},
		"$Forwarded": {},
	}
	var out []string
	for _, f := range flags {
		f = strings.TrimSpace(f)
		if _, ok := allowed[f]; ok {
			out = append(out, f)
		}
	}
	return out
}
