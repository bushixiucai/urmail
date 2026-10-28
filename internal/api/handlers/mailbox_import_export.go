package handlers

import (
	"mime"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/mailimport"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// ExportMBOX exports only explicitly selected original UIDs from one generation.
// Standard mboxrd framing is delegated to the same parser's round-trip contract.
func (h *MailboxImports) ExportMBOX(c *gin.Context) {
	if _, ok := h.principal(c); !ok || !h.slot(c) {
		return
	}
	defer func() { <-h.slots }()
	folder, ok := h.Native.folderParam(c)
	if !ok {
		return
	}
	validity, e := strconv.ParseUint(c.Query("uid_validity"), 10, 32)
	parts := strings.Split(c.Query("uids"), ",")
	if e != nil || validity == 0 || len(parts) == 0 || len(parts) > mailimport.MaxItems {
		authError(c, 400, "MAIL_IMPORT_SELECTION_INVALID")
		return
	}
	selected := make([]uint32, 0, len(parts))
	seen := map[uint32]bool{}
	for _, part := range parts {
		value, e := strconv.ParseUint(part, 10, 32)
		if e != nil || value == 0 || seen[uint32(value)] {
			authError(c, 400, "MAIL_IMPORT_SELECTION_INVALID")
			return
		}
		seen[uint32(value)] = true
		selected = append(selected, uint32(value))
	}
	client, _, ok := h.Native.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	if !mailboxExpectedValidity(c, client, folder, uint32(validity), true) {
		return
	}
	accessor, ok := client.(webmail.RawMessageAccessor)
	if !ok {
		authError(c, 503, "MAIL_IMPORT_UNAVAILABLE")
		return
	}
	rawMessages := make([][]byte, 0, len(selected))
	total := 0
	for _, uid := range selected {
		raw, current, e := accessor.GetRawMessage(c.Request.Context(), folder, uid)
		if e != nil {
			authError(c, 502, "IMAP_UNAVAILABLE")
			return
		}
		if uint64(current) != validity {
			authError(c, 409, "MAIL_IMPORT_SOURCE_CHANGED")
			return
		}
		total += len(raw)
		if total > mailimport.MaxSourceBytes || len(raw) > mailimport.MaxMessageBytes {
			authError(c, 413, "MAIL_IMPORT_EXPORT_TOO_LARGE")
			return
		}
		rawMessages = append(rawMessages, raw)
	}
	if !mailboxExpectedValidity(c, client, folder, uint32(validity), true) {
		return
	}
	exported, e := mailimport.ExportMBOX(rawMessages)
	if e != nil {
		authError(c, 422, "MAIL_IMPORT_MBOX_UNSUPPORTED")
		return
	}
	if len(exported) > mailimport.MaxSourceBytes {
		authError(c, 413, "MAIL_IMPORT_EXPORT_TOO_LARGE")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "mailbox-export.mbox"}))
	c.Data(200, "application/mbox", exported)
}
