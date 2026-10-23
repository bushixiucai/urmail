package policy

import (
	"context"
	"hash/fnv"
	"strings"
	"sync"
	"time"
)

// QuotaChecker provides mailbox daily sending limit check.
type QuotaChecker interface {
	// CheckAndIncrement verifies if the sender mailbox has remaining daily message quota.
	// Returns allowed=true if within limit, allowed=false if quota exceeded.
	CheckAndIncrement(ctx context.Context, mailboxAddress string) (allowed bool, err error)
}

// dedupCacheSize and dedupCacheTTL bound the per-message deduplication window:
// Postfix evaluates check_policy_service once per recipient, so a single
// message (queue_id) must only consume quota/rate-limit once per identity.
const (
	dedupCacheSize = 4096
	dedupCacheTTL  = 10 * time.Minute
)

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
	QuotaChecker   QuotaChecker
	InboundChecker interface {
		CheckRecipient(context.Context, string) (bool, error)
	}
	messageLocks [64]sync.Mutex
	RateLimiter  *RateLimiter

	// dedup suppresses duplicate quota/rate accounting for the same
	// (identity, queue_id) pair within a short window. nil disables dedup.
	dedup *seenCache
}

// NewService creates a Service with per-message deduplication enabled.
func NewService() *Service {
	return &Service{dedup: newSeenCache(dedupCacheSize, dedupCacheTTL)}
}

// Evaluate evaluates an incoming Postfix request.
//
// The rate-limit/quota identity is the SASL username when present, otherwise
// the client IP address. Postfix invokes the policy service once per
// recipient, so evaluations sharing the same (identity, queue_id) are
// counted exactly once.
func (s *Service) Evaluate(ctx context.Context, req *Request) Decision {
	state := strings.ToUpper(req.Attrs["protocol_state"])
	if s.InboundChecker != nil && req.Recipient != "" && (state == "RCPT" || (state == "" && req.SASLUsername == "")) {
		allowed, err := s.InboundChecker.CheckRecipient(ctx, req.Recipient)
		if err != nil {
			return Decision{Action: ActionDefer, Reason: "Temporary subscription lookup failure"}
		}
		if !allowed {
			return Decision{Action: ActionDefer, Reason: "Recipient subscription expired or suspended; renewal required"}
		}
	}
	if req.SASLUsername == "" {
		return Decision{Action: ActionDunno}
	}
	// Only DATA is charged: MAIL has no reliable queue id and RCPT repeats.
	if state != "" && state != "DATA" {
		return Decision{Action: ActionDunno}
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(req.SASLUsername + "\x00" + req.QueueID))
	lock := &s.messageLocks[hash.Sum32()%uint32(len(s.messageLocks))]
	lock.Lock()
	defer lock.Unlock()
	identity := req.SASLUsername
	// smtpd_sender_restrictions runs at MAIL FROM, before Postfix assigns a
	// queue id. In that phase a missing queue id means "one message per MAIL
	// FROM", so duplicate suppression must be disabled; otherwise every
	// following message in the TTL window is silently exempted.
	dedupKey := identity + "\x00" + req.QueueID
	dedup := s.dedup != nil && req.QueueID != ""
	if dedup && s.dedup.Seen(dedupKey) {
		return Decision{Action: ActionDunno}
	}
	forget := func() {
		if dedup {
			s.dedup.Forget(dedupKey)
		}
	}

	if s.RateLimiter != nil {
		if !s.RateLimiter.Allow(identity) {
			forget()
			return Decision{
				Action: ActionReject,
				Reason: "Rate limit exceeded, please retry later",
			}
		}
	}

	if s.QuotaChecker != nil {
		allowed, err := s.QuotaChecker.CheckAndIncrement(ctx, identity)
		if err != nil {
			forget()
			return Decision{
				Action: ActionDefer,
				Reason: "Temporary quota service failure",
			}
		}
		if !allowed {
			forget()
			return Decision{
				Action: ActionReject,
				Reason: "Daily message quota exceeded",
			}
		}
	}

	return Decision{Action: ActionDunno}
}
