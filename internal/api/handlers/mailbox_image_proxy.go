package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/imageproxy"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
)

// imageProxyPath is the route the sanitizer points remote images at.
const imageProxyPath = "/api/v1/mailbox/images/proxy"

// ImageProxy serves a remote mail image through the server.
//
// The reader's browser never contacts the third-party host: the fetch
// happens here, so the tracking pixel only sees the server's address. The
// request is authorized by the HMAC signature that the sanitizer embedded
// in the image URL; the signature binds the mailbox, the message UID, the
// target URL and an expiry.
//
// Image loads originate from a sandboxed frame with an opaque origin, so
// they cannot present the mailbox bearer token. The signature is therefore
// the primary gate. A bearer token that is present anyway must belong to
// the same mailbox, so a caller can never use one mailbox's signature to
// read another mailbox's mail.
func (h *MailboxNativeHandler) ImageProxy(c *gin.Context) {
	signer := imageproxy.NewSigner(h.RenderKey)
	if !signer.Enabled() {
		authError(c, http.StatusServiceUnavailable, "WEBMAIL_DISABLED")
		return
	}
	payload, err := signer.Verify(c.Query("u"), c.Query("sig"))
	if err != nil {
		authError(c, http.StatusForbidden, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	if !h.imageProxySessionAllowed(c, payload.MailboxID) {
		authError(c, http.StatusForbidden, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	fetcher := h.ImageFetcher
	if fetcher == nil {
		fetcher = &imageproxy.Fetcher{}
	}
	data, contentType, fetchErr := fetcher.Fetch(c.Request.Context(), payload.URL)
	if fetchErr != nil {
		// Degrade to a transparent placeholder: the message keeps its
		// layout and the reader sees no broken-image box.
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Cache-Control", "private, no-store")
		c.Data(http.StatusOK, "image/png", imageproxy.PlaceholderPNG)
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, max-age=600")
	c.Data(http.StatusOK, contentType, data)
}

// imageProxySessionAllowed verifies a bearer token when the caller sends
// one. A request without a token is authorized by its signature alone;
// a request with one must belong to the signed mailbox.
func (h *MailboxNativeHandler) imageProxySessionAllowed(c *gin.Context, mailboxID int64) bool {
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	if header == "" {
		return true
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return false
	}
	if h.Sessions == nil {
		return false
	}
	session, err := h.Sessions.Get(c.Request.Context(), mailboxprincipal.HashToken(strings.TrimSpace(parts[1])))
	if err != nil || session.MailboxID != mailboxID {
		return false
	}
	return true
}

// imageProxyRewriter returns the hook the sanitizer uses to turn an
// allowed remote image into a signed proxy URL. A signing failure drops
// the image: the proxy is the only safe way to show a remote image, so
// there is no direct-URL fallback.
func (h *MailboxNativeHandler) imageProxyRewriter(mailboxID int64, uid uint32) func(string) (string, bool) {
	signer := imageproxy.NewSigner(h.RenderKey)
	return func(remote string) (string, bool) {
		token, signature, err := signer.Sign(imageproxy.Payload{
			MailboxID: mailboxID,
			UID:       uid,
			Expires:   time.Now().Add(imageproxy.SignatureTTL).Unix(),
			URL:       remote,
		})
		if err != nil {
			return "", false
		}
		return imageProxyPath + "?u=" + token + "&sig=" + signature, true
	}
}
