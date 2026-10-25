package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/webmail"
)

// ListMessages returns a page of UID summaries for a folder.
func (h *MailboxNativeHandler) ListMessages(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()

	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	result, err := client.ListMessages(c.Request.Context(), folder, webmail.ListOpts{Limit: limit, Offset: offset})
	if err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	if result.Messages == nil {
		result.Messages = []webmail.MessageSummary{}
	}
	c.JSON(http.StatusOK, gin.H{
		"messages": result.Messages,
		"total":    result.Total,
		"limit":    webmail.ClampListOpts(webmail.ListOpts{Limit: limit, Offset: offset}).Limit,
		"offset":   webmail.ClampListOpts(webmail.ListOpts{Limit: limit, Offset: offset}).Offset,
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
