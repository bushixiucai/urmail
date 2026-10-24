package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// windowLimiter is an in-memory sliding-window throughput limiter. A
// single-process limiter is sufficient while urmaild runs as one instance;
// a scale-out deployment must move the counter into PostgreSQL or Redis.
type windowLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

const maxRateLimitEntries = 20000

func newWindowLimiter() *windowLimiter {
	return &windowLimiter{hits: make(map[string][]time.Time)}
}

func (l *windowLimiter) allow(key string, limit int, window time.Duration) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > maxRateLimitEntries {
		// Bound memory when a distributed client sprays unique keys.
		l.hits = make(map[string][]time.Time)
	}
	cutoff := now.Add(-window)
	kept := l.hits[key][:0]
	for _, ts := range l.hits[key] {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

// TenantRateKey keys the limiter on the authenticated tenant so one tenant's
// spend cannot exhaust another tenant's budget.
func TenantRateKey(c *gin.Context) (string, bool) {
	tenantID, ok := GetTenantID(c)
	if !ok {
		return "", false
	}
	return "tenant|" + strconv.FormatInt(tenantID, 10), true
}

// RateLimitWindow allows at most limit requests per key per window. A key
// function returning false skips the request (the route enforces its own
// authentication). Billing quote/checkout need this because a tenant could
// otherwise flood pending orders and, with a reserved coupon, burn global
// quota (M5 review 3.27).
func RateLimitWindow(limit int, window time.Duration, key func(*gin.Context) (string, bool)) gin.HandlerFunc {
	limiter := newWindowLimiter()
	return func(c *gin.Context) {
		k, ok := key(c)
		if !ok {
			c.Next()
			return
		}
		if !limiter.allow(k, limit, window) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": gin.H{"code": "RATE_LIMITED"}})
			return
		}
		c.Next()
	}
}
