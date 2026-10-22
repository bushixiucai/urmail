package middleware

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/store"
)

// CSRFHeader is the header carrying the synchronizer CSRF token.
const CSRFHeader = "X-CSRF-Token"

// RequireCSRF verifies Origin/Referer matches the Host and X-CSRF-Token matches the active session.
func RequireCSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}

		val, exists := c.Get(ContextSession)
		if !exists {
			// If not an authenticated session, let the auth middleware or handler reject
			c.Next()
			return
		}

		session, ok := val.(store.Sessions)
		if !ok || session.CsrfToken == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{"code": "CSRF_ERROR", "message": "auth.csrf_missing"},
			})
			return
		}

		// 1. Validate Origin or Referer against request Host
		reqHost := c.Request.Host
		origin := c.GetHeader("Origin")
		if origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, reqHost) {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"error": gin.H{"code": "CSRF_INVALID_ORIGIN", "message": "auth.csrf_invalid_origin"},
				})
				return
			}
		} else {
			referer := c.GetHeader("Referer")
			if referer != "" {
				u, err := url.Parse(referer)
				if err != nil || !strings.EqualFold(u.Host, reqHost) {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
						"error": gin.H{"code": "CSRF_INVALID_ORIGIN", "message": "auth.csrf_invalid_origin"},
					})
					return
				}
			}
		}

		// 2. Validate X-CSRF-Token against session token
		inboundToken := c.GetHeader(CSRFHeader)
		if inboundToken == "" || subtle.ConstantTimeCompare([]byte(inboundToken), []byte(session.CsrfToken)) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{"code": "CSRF_INVALID", "message": "auth.csrf_invalid"},
			})
			return
		}

		c.Next()
	}
}
