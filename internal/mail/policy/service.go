package policy

import (
	"context"
	"sync"
	"time"
)

// QuotaChecker provides mailbox daily sending limit check.
type QuotaChecker interface {
	// CheckAndIncrement verifies if the sender mailbox has remaining daily message quota.
	// Returns allowed=true if within limit, allowed=false if quota exceeded.
	CheckAndIncrement(ctx context.Context, mailboxAddress string) (allowed bool, err error)
}

// RateLimiter tracks requests per time window in memory.
type RateLimiter struct {
	mu        sync.Mutex
	counts    map[string]int
	window    time.Duration
	maxLimit  int
	lastReset time.Time
}

// NewRateLimiter creates a RateLimiter with limit per window.
func NewRateLimiter(maxLimit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		counts:    make(map[string]int),
		window:    window,
		maxLimit:  maxLimit,
		lastReset: time.Now(),
	}
}

// Allow checks if the key is allowed and increments usage counter.
func (r *RateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if now.Sub(r.lastReset) > r.window {
		r.counts = make(map[string]int)
		r.lastReset = now
	}

	r.counts[key]++
	return r.counts[key] <= r.maxLimit
}

// Service handles Postfix policy evaluation.
type Service struct {
	QuotaChecker QuotaChecker
	RateLimiter  *RateLimiter
}

// Evaluate evaluates an incoming Postfix request.
func (s *Service) Evaluate(ctx context.Context, req *Request) Decision {
	// 1. If sending as authenticated user (SASL), check rate limit and daily quota
	if req.SASLUsername != "" {
		if s.RateLimiter != nil {
			if !s.RateLimiter.Allow(req.SASLUsername) {
				return Decision{
					Action: ActionReject,
					Reason: "Rate limit exceeded, please retry later",
				}
			}
		}

		if s.QuotaChecker != nil {
			allowed, err := s.QuotaChecker.CheckAndIncrement(ctx, req.SASLUsername)
			if err != nil {
				// Fail closed or defer on DB error
				return Decision{
					Action: ActionDefer,
					Reason: "Temporary quota service failure",
				}
			}
			if !allowed {
				return Decision{
					Action: ActionReject,
					Reason: "Daily message quota exceeded",
				}
			}
		}
	}

	// Default permit
	return Decision{Action: ActionDunno}
}
