package handlers

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/linkconfirm"
)

const linkConfirmPath = "/api/v1/mailbox/links/confirm"

// LinkConfirm shows the confirmation page for one signed outbound link
// (GET) or redirects to the real target after the reader clicks through
// (POST). The page is the only place that reveals the destination, and
// the hostname is rendered character by character so look-alike domains
// cannot hide inside a familiar word.
func (h *MailboxNativeHandler) LinkConfirm(c *gin.Context) {
	signer := linkconfirm.NewSigner(h.RenderKey)
	if !signer.Enabled() {
		authError(c, http.StatusServiceUnavailable, "WEBMAIL_DISABLED")
		return
	}
	token := strings.TrimSpace(c.Query("t"))
	if token == "" {
		token = strings.TrimSpace(c.PostForm("t"))
	}
	payload, err := signer.Verify(token)
	if err != nil {
		h.linkConfirmError(c, http.StatusForbidden)
		return
	}
	target, err := url.Parse(payload.URL)
	if err != nil || !linkconfirm.NeedsConfirm(payload.URL) {
		h.linkConfirmError(c, http.StatusBadRequest)
		return
	}
	switch c.Request.Method {
	case http.MethodPost:
		c.Header("Cache-Control", "no-store")
		c.Header("Referrer-Policy", "no-referrer")
		c.Redirect(http.StatusFound, payload.URL)
	default:
		h.linkConfirmPage(c, payload, target, token)
	}
}

func (h *MailboxNativeHandler) linkConfirmError(c *gin.Context, status int) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	c.Data(status, "text/html; charset=utf-8", []byte(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Link unavailable</title><main style="font-family:system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 1rem"><h1>Link unavailable</h1><p>This confirmation link is missing, expired or was not issued by this server.</p></main></html>`))
}

func (h *MailboxNativeHandler) linkConfirmPage(c *gin.Context, payload linkconfirm.Payload, target *url.URL, token string) {
	host := target.Hostname()
	var hostHTML strings.Builder
	for _, ch := range linkconfirm.HostLabels(host) {
		hostHTML.WriteString(`<span class="ch">`)
		hostHTML.WriteString(html.EscapeString(ch))
		hostHTML.WriteString(`</span>`)
	}
	from := html.EscapeString(payload.From)
	if from == "" {
		from = "(unknown sender)"
	}
	subject := html.EscapeString(payload.Subject)
	if subject == "" {
		subject = "(no subject)"
	}
	fullURL := html.EscapeString(payload.URL)
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	body := `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Confirm external link</title>` +
		`<style>body{font-family:system-ui,sans-serif;margin:0;background:#faf9f5;color:#1a1a1a}` +
		`main{max-width:40rem;margin:2rem auto;padding:0 1rem}` +
		`.host{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:1.25rem;letter-spacing:.08em;word-break:break-all}` +
		`.ch{display:inline-block}` +
		`.url{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:.85rem;word-break:break-all;background:#fff;border:1px solid #ddd;padding:.75rem;border-radius:.5rem}` +
		`.meta{color:#555;font-size:.9rem}` +
		`.warn{background:#fff7e6;border:1px solid #f0c36d;padding:.75rem 1rem;border-radius:.5rem;margin:1rem 0}` +
		`form{display:flex;gap:.75rem;flex-wrap:wrap;margin-top:1.25rem}` +
		`button{appearance:none;border:0;border-radius:.5rem;padding:.7rem 1.1rem;font:inherit;cursor:pointer}` +
		`button.primary{background:#1a1a1a;color:#fff}` +
		`a.cancel{align-self:center;color:#555;text-decoration:none}</style>` +
		`<main><h1>Leave urMail?</h1>` +
		`<p class="warn">You are about to open a link from an email. Check the domain carefully — attackers often use look-alike characters.</p>` +
		`<p>Destination domain</p><p class="host" aria-label="` + html.EscapeString(host) + `">` + hostHTML.String() + `</p>` +
		`<p>Full URL</p><p class="url">` + fullURL + `</p>` +
		`<p class="meta">From: ` + from + `<br>Subject: ` + subject + `</p>` +
		`<form method="post" action="` + linkConfirmPath + `">` +
		`<input type="hidden" name="t" value="` + html.EscapeString(token) + `">` +
		`<button class="primary" type="submit">Continue to site</button>` +
		`<span class="cancel">You can close this tab to cancel</span>` +
		`</form></main></html>`
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(body))
}

// linkConfirmRewriter returns the hook the sanitizer uses to turn an
// absolute http(s) href into a same-origin confirmation URL. A signing
// failure drops the href: there is no direct-URL fallback.
func (h *MailboxNativeHandler) linkConfirmRewriter(mailboxID int64, uid uint32, from, subject string) func(string) (string, bool) {
	signer := linkconfirm.NewSigner(h.RenderKey)
	return func(href string) (string, bool) {
		if !linkconfirm.NeedsConfirm(href) {
			return "", false
		}
		token, err := signer.Sign(linkconfirm.Payload{
			MailboxID: mailboxID,
			UID:       uid,
			Expires:   time.Now().Add(linkconfirm.SignatureTTL).Unix(),
			URL:       href,
			From:      from,
			Subject:   subject,
		})
		if err != nil {
			return "", false
		}
		return linkConfirmPath + "?t=" + url.QueryEscape(token), true
	}
}
