package handlers

import (
	"html"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/i18n"
)

// Confirmation never changes consent: mail scanners may safely follow its GET.
func (h *MarketingSuppressionHandler) Confirmation(c *gin.Context) {
	if h.Tokens == nil {
		h.unsubscribePage(c, http.StatusServiceUnavailable, "marketing.unsubscribe.unavailable", "")
		return
	}
	token := c.Query("token")
	if _, _, valid := h.Tokens.VerifyUnsubscribeToken(token); !valid {
		h.unsubscribePage(c, http.StatusBadRequest, "marketing.unsubscribe.invalid", "")
		return
	}
	h.unsubscribePage(c, http.StatusOK, "marketing.unsubscribe.prompt", token)
}

func (h *MarketingSuppressionHandler) unsubscribePage(c *gin.Context, status int, messageKey, token string) {
	catalogs := h.Messages
	if catalogs == nil {
		var err error
		catalogs, err = i18n.NewStore()
		if err != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
	}
	locale := "zh-CN"
	if c.Query("locale") == "en" || strings.HasPrefix(strings.ToLower(c.GetHeader("Accept-Language")), "en") {
		locale = "en"
	}
	catalog := catalogs.Get(locale)
	title := html.EscapeString(catalog.Lookup("marketing.unsubscribe.title", nil))
	message := html.EscapeString(catalog.Lookup(messageKey, nil))
	form := ""
	if token != "" {
		form = `<form method="post" action="/api/v1/marketing/unsubscribe"><input type="hidden" name="token" value="` + html.EscapeString(token) + `"><input type="hidden" name="confirm_unsubscribe" value="1"><button type="submit">` + html.EscapeString(catalog.Lookup("marketing.unsubscribe.confirm", nil)) + `</button></form>`
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Content-Security-Policy", "default-src 'none'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	c.Data(status, "text/html; charset=utf-8", []byte(`<!doctype html><html lang="`+locale+`"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>`+title+`</title><main><h1>`+title+`</h1><p>`+message+`</p>`+form+`</main></html>`))
}
