package handlers

import (
	"context"
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
	From        string                  `json:"from"`
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
	// From is never client-trusted: it must be the session address or an
	// alias-sender grant reviewed for this mailbox. Fail closed when no
	// authorization source is configured.
	from := normalizeFromCandidate(req.From)
	if from == "" {
		from = session.Address
	}
	if from != session.Address {
		allowed := false
		if h.SelfSettings != nil {
			granted, err := h.SelfSettings.SenderIdentityAllowed(c.Request.Context(), session.TenantID, session.Address, from)
			if err != nil {
				authError(c, http.StatusBadGateway, "MAIL_POLICY_UNAVAILABLE")
				return
			}
			allowed = granted
		}
		if !allowed {
			authError(c, http.StatusForbidden, "MAILBOX_FROM_NOT_AUTHORIZED")
			return
		}
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
	if h.MaySend != nil {
		allowed, err := h.MaySend(c.Request.Context(), session.TenantID, session.MailboxID)
		if err != nil {
			authError(c, http.StatusBadGateway, "MAIL_POLICY_UNAVAILABLE")
			return
		}
		if !allowed {
			authError(c, http.StatusForbidden, "MAIL_SENDING_SUSPENDED")
			return
		}
	}
	password, ok := h.vaultPassword(c)
	if !ok {
		return
	}
	submission, err := h.Submitter.Send(c.Request.Context(), session.Address, password, webmail.Outgoing{
		From:        from,
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
	// SMTP has accepted the message. A Sent failure is reported, never retried as another send.
	if h.Dialer != nil && len(submission.Raw) > 0 {
		if client, _, ok := h.dialIMAP(c); ok {
			defer func() { _ = client.Close() }()
			if _, copyErr := client.SaveSentCopy(c.Request.Context(), submission.Raw); copyErr != nil {
				c.JSON(http.StatusOK, gin.H{"sent": true, "sent_copy": "pending"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"sent": true, "sent_copy": "saved"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"sent": true, "sent_copy": "pending"})
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
