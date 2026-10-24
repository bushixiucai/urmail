package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{Name: "urmail_http_requests_total", Help: "Requests by bounded route template, method and status class."}, []string{"route", "method", "status_class"})
var httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "urmail_http_request_duration_seconds", Help: "HTTP duration by bounded route template.", Buckets: prometheus.DefBuckets}, []string{"route", "method"})

// HTTPMetrics belongs outside Recover so a recovered panic is recorded as a
// server error. Raw paths, queries, tenant identifiers and bodies are excluded.
func HTTPMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		method := c.Request.Method
		switch method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		default:
			method = "OTHER"
		}
		status := c.Writer.Status()
		class := "unknown"
		if status >= 100 && status < 600 {
			class = strconv.Itoa(status/100) + "xx"
		}
		httpRequests.WithLabelValues(route, method, class).Inc()
		httpDuration.WithLabelValues(route, method).Observe(time.Since(started).Seconds())
	}
}
