package handlers

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type mailboxSendAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	ContentB64  string `json:"content_base64"`
}

type mailboxSendRequest struct {
	To          []string                `json:"to"`
	Cc          []string                `json:"cc"`
	Bcc         []string                `json:"bcc"`
	Subject     string                  `json:"subject"`
	Text        string                  `json:"text"`
	HTML        string                  `json:"html"`
	Attachments []mailboxSendAttachment `json:"attachments"`
}

const maxSendAttachmentBytes = 5 << 20
const maxSendTotalAttachmentBytes = 10 << 20
const maxSendAttachments = 10

// SendMessage submits mail via local SMTP AUTH using the vaulted password.
func (h *MailboxNativeHandler) SendMessage(c *gin.Context) {
	if !h.requireEnabled(c) {
		return
	}
	if h.Submitter == nil || h.Vault == nil {
		authError(c, http.StatusServiceUnavailable, "WEBMAIL_DISABLED")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	var req mailboxSendRequest
	if !bindMailboxMessage(c, &req) {
		return
	}
	to := cleanAddrs(req.To)
	cc := cleanAddrs(req.Cc)
	bcc := cleanAddrs(req.Bcc)
	if len(to)+len(cc)+len(bcc) == 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
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
	password, ok := h.vaultPassword(c)
	if !ok {
		return
	}
	err := h.Submitter.Send(c.Request.Context(), session.Address, password, webmail.Outgoing{
		From:        session.Address,
		To:          to,
		Cc:          cc,
		Bcc:         bcc,
		Subject:     req.Subject,
		Text:        req.Text,
		HTML:        req.HTML,
		Attachments: atts,
	})
	if err != nil {
		if errors.Is(err, webmail.ErrSubmitUncertain) {
			authError(c, http.StatusConflict, "MAILBOX_SEND_UNCERTAIN")
			return
		}
		authError(c, http.StatusBadGateway, "SMTP_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"sent": true})
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
