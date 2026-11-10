package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/webmail"
)

// ThreadMembers serves GET /api/v1/mailbox/folders/:name/messages/:uid/thread
// — the cross-folder member list of the thread containing (folder, uid).
// Contract: Shared/design/THREAD-MEMBERS-API-CONTRACT.md (v0.6.20 task ②).
func (h *MailboxNativeHandler) ThreadMembers(c *gin.Context) {
	folder := c.Param("name")
	uid64, err := strconv.ParseUint(strings.TrimSpace(c.Param("uid")), 10, 32)
	if err != nil || uid64 == 0 {
		authError(c, http.StatusBadRequest, "INVALID_UID")
		return
	}
	limit64, _ := strconv.ParseUint(c.Query("limit"), 10, 32)
	anchor64, _ := strconv.ParseUint(c.Query("anchor_uid"), 10, 32)
	before := strings.TrimSpace(c.Query("before"))
	if before != "" && anchor64 != 0 {
		authError(c, http.StatusBadRequest, "INVALID_THREAD_QUERY")
		return
	}

	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()

	opts := webmail.ClampThreadMembersOpts(webmail.ThreadMembersOpts{
		Limit:     int(limit64),
		AnchorUID: uint32(anchor64),
		Before:    before,
	})
	result, err := client.GetThreadMembers(c.Request.Context(), folder, uint32(uid64), opts)
	if err != nil {
		switch {
		case errors.Is(err, webmail.ErrThreadMemberNotFound):
			authError(c, http.StatusNotFound, "MESSAGE_NOT_FOUND")
		case errors.Is(err, webmail.ErrInvalidThreadCursor):
			authError(c, http.StatusBadRequest, "INVALID_THREAD_QUERY")
		default:
			authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		}
		return
	}
	// Member windows change (flags/reads) — short client cache only.
	c.Header("Cache-Control", "private, max-age=10")
	c.JSON(http.StatusOK, result)
}
