package middleware

import (
	"crypto/subtle"
	"net"
	"strings"

	"github.com/gin-gonic/gin"
)

// MetricsAccess grants only machine metrics to an in-network scraper that
// carries its own dedicated credential. The source must be loopback (the
// historical posture) or a private-network address (the compose bridge, so
// Prometheus can scrape /internal/metrics without sharing urmaild's
// loopback namespace — GRAFANA-DESIGN §2.1 option A). The bearer-token gate
// is unchanged: length floor, no whitespace, constant-time compare. It
// never creates a management or mailbox session.
func MetricsAccess(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
		if err != nil {
			host = c.Request.RemoteAddr
		}
		ip := net.ParseIP(host)
		supplied := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if len(token) < 32 || !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || strings.ContainsAny(token, " \r\n\t") || err != nil || ip == nil || !isAllowedMetricsSource(ip) || subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) != 1 {
			c.AbortWithStatus(403)
			return
		}
		c.Next()
	}
}

// isAllowedMetricsSource permits loopback and private-network scrapers
// (RFC1918 for the compose bridge, ULA for IPv6 networks). Link-local and
// public sources stay rejected: the compose topology never places the
// scraper behind a NAT that would present a link-local source.
func isAllowedMetricsSource(ip net.IP) bool {
	return ip.IsLoopback() || (ip.IsPrivate() && !ip.IsLinkLocalUnicast())
}
