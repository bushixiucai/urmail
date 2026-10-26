package handlers

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// loginLimiter is an in-memory sliding-window rate limiter for login
// attempts, keyed by client IP + email. It bounds online password
// guessing per identity without external state. A single-process limiter
// is sufficient while urmaild runs as one instance; when the deployment
// scales out this must move to PostgreSQL or Redis.
type loginLimiter struct {
	mu    sync.Mutex
	tries map[string][]time.Time
}

const (
	loginMaxFailures   = 5
	loginMaxIPFailures = 50
	loginMaxEntries    = 10000
	loginWindow        = 15 * time.Minute
)

var loginAttempts = newLoginLimiter()

// mfaDisableAttempts bounds online guessing of the TOTP/recovery code on the
// MFA-disable endpoint (audit AUTH-01). The budget is tighter than login
// because a 6-digit TOTP with ±1 skew has only ~3 valid codes per window.
var mfaDisableAttempts = newLoginLimiter()

const mfaDisableMaxFailures = 5

// mfaDisableKey namespaces the per-user failure budget for the disable path.
func mfaDisableKey(userID int64) string {
	return "mfa-disable|" + strconv.FormatInt(userID, 10)
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{tries: make(map[string][]time.Time)}
}

// allow reports whether a new attempt for key is permitted.
func (l *loginLimiter) allow(key string) bool {
	return l.allowN(key, loginMaxFailures)
}

// allowN is allow with an explicit failure budget (used for the looser
// per-IP budget that catches password spraying across many accounts).
func (l *loginLimiter) allowN(key string, budget int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key)
	return len(l.tries[key]) < budget
}

// ipKeyFromLimiterKey turns "ip|email" into "ip|ip"; the login key already
// starts with the client address.
func ipKeyFromLimiterKey(limiterKey string) string {
	if i := strings.IndexByte(limiterKey, '|'); i >= 0 {
		return "ip|" + limiterKey[:i]
	}
	return "ip|" + limiterKey
}

// fail records a failed attempt for key.
func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key)
	if len(l.tries) >= loginMaxEntries {
		if _, exists := l.tries[key]; !exists {
			// Bound memory under a distributed spray of unique keys.
			l.tries = make(map[string][]time.Time)
		}
	}
	l.tries[key] = append(l.tries[key], time.Now())
}

// reset clears the failure history for key after a successful login.
func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.tries, key)
}

// prune drops timestamps outside the window for key.
func (l *loginLimiter) prune(key string) {
	cutoff := time.Now().Add(-loginWindow)
	kept := l.tries[key][:0]
	for _, ts := range l.tries[key] {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) == 0 {
		delete(l.tries, key)
		return
	}
	l.tries[key] = kept
}
