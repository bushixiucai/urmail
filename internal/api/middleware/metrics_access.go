package middleware

import (
	"crypto/subtle"
	"net"
	"strings"

	"github.com/gin-gonic/gin"
)

// MetricsAccess grants only machine metrics to a loopback scraper with its own
// dedicated credential. It never creates a management or mailbox session.
func MetricsAccess(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
		if err != nil {
			host = c.Request.RemoteAddr
		}
		ip := net.ParseIP(host)
		supplied := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if len(token) < 32 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || strings.ContainsAny(token, " \r\n\t") || err != nil || ip == nil || !ip.IsLoopback() || subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) != 1 {
			c.AbortWithStatus(403)
			return
		}
		c.Next()
	}
}
