package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// BodyLimit caps request bodies before JSON decoding. This keeps a single
// tenant from forcing the process to buffer an unbounded payload.
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

// BodyLimitWithMailboxUploads preserves the default limit except for the two
// authenticated native-mail upload routes. Wrapping is lazy: authentication can
// reject a request without reading any of its body.
func BodyLimitWithMailboxUploads(defaultBytes, uploadBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		maxBytes := defaultBytes
		if (c.Request.Method == http.MethodPost && c.Request.URL.Path == "/api/v1/mailbox/send") ||
			(c.Request.Method == http.MethodPut && c.Request.URL.Path == "/api/v1/mailbox/drafts") {
			maxBytes = uploadBytes
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}
