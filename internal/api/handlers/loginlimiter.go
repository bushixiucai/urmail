package handlers

import (
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
	loginMaxFailures = 5
	loginWindow      = 15 * time.Minute
)

var loginAttempts = newLoginLimiter()

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{tries: make(map[string][]time.Time)}
}

// allow reports whether a new attempt for key is permitted.
func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key)
	return len(l.tries[key]) < loginMaxFailures
}

// fail records a failed attempt for key.
func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key)
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
