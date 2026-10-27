package mailguard

import (
	"container/list"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"
)

// Scope namespaces a failure budget. The same source address can be banned
// for one protocol without affecting the others, so an attacker cannot
// exhaust a mailbox's IMAP access by failing SMTP AUTH (and vice versa).
type Scope string

const (
	// ScopeSMTPAuth covers submission/smtps SASL authentication.
	ScopeSMTPAuth Scope = "smtp-auth"
	// ScopeIMAPAuth covers IMAP LOGIN/AUTHENTICATE.
	ScopeIMAPAuth Scope = "imap-auth"
	// ScopePOP3Auth covers POP3 USER/PASS and AUTH.
	ScopePOP3Auth Scope = "pop3-auth"
	// ScopeWebLogin covers the browser/API login endpoint.
	ScopeWebLogin Scope = "web-login"
)

// Config is the failure policy of one Limiter.
//
// MaxFailures failures inside Window ban the source until BanDuration has
// passed. Bans are never permanent. MaxSources bounds memory: when the
// population is full the least recently seen source is evicted, preferring
// sources whose state has already expired.
type Config struct {
	MaxFailures int
	Window      time.Duration
	BanDuration time.Duration
	MaxSources  int
	// Now is the clock used for windows and bans. Nil means time.Now. Tests
	// inject a monotonic fake clock; production code should leave it nil so
	// time.Time's monotonic reading is used.
	Now func() time.Time
}

// ErrInvalidConfig is returned by New when a policy is incomplete or
// non-positive.
var ErrInvalidConfig = errors.New("mailguard: invalid limiter config")

// DefaultConfig returns the baseline protocol policy: 10 failures in 10
// minutes ban the source for 30 minutes. It matches the conservative values
// rendered into Postfix (smtpd_client_auth_rate_limit) and Dovecot
// (auth_failure_delay plus anvil auth penalty).
func DefaultConfig() Config {
	return Config{
		MaxFailures: 10,
		Window:      10 * time.Minute,
		BanDuration: 30 * time.Minute,
		MaxSources:  4096,
	}
}

// DefaultConfigFor returns the recommended policy for one boundary.
//
// Browser logins get a tighter budget because a human only needs a couple of
// attempts; mail clients retry automatically (and some shared/NAT addresses
// serve many users), so they get a slightly wider budget with the same
// non-permanent ban duration.
func DefaultConfigFor(scope Scope) Config {
	cfg := DefaultConfig()
	if scope == ScopeWebLogin {
		cfg.MaxFailures = 5
		cfg.Window = 15 * time.Minute
		cfg.BanDuration = 15 * time.Minute
	}
	return cfg
}

// Status is a diagnostic snapshot for one source in one scope. It exists so
// a support operator can answer "why is this account blocked?" without
// reading process memory.
type Status struct {
	Scope      Scope
	Source     netip.Addr
	Failures   int
	Banned     bool
	RetryAfter time.Duration
}

type limiterKey struct {
	scope Scope
	addr  netip.Addr
}

type record struct {
	scope       Scope
	addr        netip.Addr
	failures    []time.Time
	bannedUntil time.Time
}

// Limiter is a concurrent, per-source, sliding-window authentication failure
// limiter. It holds no global state: create one instance per process (or per
// boundary with a different policy) and share it through the usual
// dependency injection.
type Limiter struct {
	mu      sync.Mutex
	cfg     Config
	now     func() time.Time
	entries map[limiterKey]*list.Element
	lru     *list.List // front = most recently seen
}

// New validates cfg and returns an empty limiter.
func New(cfg Config) (*Limiter, error) {
	switch {
	case cfg.MaxFailures <= 0:
		return nil, fmt.Errorf("%w: MaxFailures must be positive", ErrInvalidConfig)
	case cfg.Window <= 0:
		return nil, fmt.Errorf("%w: Window must be positive", ErrInvalidConfig)
	case cfg.BanDuration <= 0:
		return nil, fmt.Errorf("%w: BanDuration must be positive (permanent bans are not supported)", ErrInvalidConfig)
	case cfg.MaxSources <= 0:
		return nil, fmt.Errorf("%w: MaxSources must be positive", ErrInvalidConfig)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		cfg:     cfg,
		now:     now,
		entries: make(map[limiterKey]*list.Element, 64),
		lru:     list.New(),
	}, nil
}

// Failure records one failed authentication for source. The MaxFailures-th
// failure inside Window bans the source; further failures while banned are
// ignored so a ban always expires after exactly BanDuration and can never be
// extended into a permanent one. An invalid address cannot be tracked and is
// ignored; callers should reject such requests by another means.
func (l *Limiter) Failure(scope Scope, addr netip.Addr) {
	if !addr.IsValid() {
		return
	}
	addr = normalizeAddr(addr)

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	rec := l.getOrCreateLocked(scope, addr, now)
	if !rec.bannedUntil.IsZero() {
		if now.Before(rec.bannedUntil) {
			return
		}
		// Expired ban: start a fresh budget.
		rec.bannedUntil = time.Time{}
		rec.failures = rec.failures[:0]
	}

	l.pruneLocked(rec, now)
	rec.failures = append(rec.failures, now)
	if len(rec.failures) >= l.cfg.MaxFailures {
		rec.bannedUntil = now.Add(l.cfg.BanDuration)
		rec.failures = rec.failures[:0]
	}
}

// Success clears all failure state for source in scope. A successful login
// proves the source is not currently guessing, so it starts from zero.
func (l *Limiter) Success(scope Scope, addr netip.Addr) {
	l.Unban(scope, addr)
}

// Banned reports whether source is currently inside a ban for scope.
func (l *Limiter) Banned(scope Scope, addr netip.Addr) bool {
	return l.Status(scope, addr).Banned
}

// RetryAfter returns the remaining ban time for source, or zero when the
// source is allowed.
func (l *Limiter) RetryAfter(scope Scope, addr netip.Addr) time.Duration {
	return l.Status(scope, addr).RetryAfter
}

// Unban deletes any failure and ban state for source in scope. Operators use
// it for shared-address recovery, e.g. an office or carrier NAT where one
// compromised device exhausted the budget for everybody else.
func (l *Limiter) Unban(scope Scope, addr netip.Addr) {
	if !addr.IsValid() {
		return
	}
	addr = normalizeAddr(addr)

	l.mu.Lock()
	defer l.mu.Unlock()
	if elem, ok := l.entries[limiterKey{scope: scope, addr: addr}]; ok {
		l.removeLocked(elem)
	}
}

// Status returns the current diagnostic state for source in scope and lazily
// expires finished bans and windows.
func (l *Limiter) Status(scope Scope, addr netip.Addr) Status {
	status := Status{Scope: scope}
	if !addr.IsValid() {
		return status
	}
	addr = normalizeAddr(addr)
	status.Source = addr

	l.mu.Lock()
	defer l.mu.Unlock()

	elem, ok := l.entries[limiterKey{scope: scope, addr: addr}]
	if !ok {
		return status
	}
	l.lru.MoveToFront(elem)
	rec := elem.Value.(*record)

	now := l.now()
	if !rec.bannedUntil.IsZero() {
		if now.Before(rec.bannedUntil) {
			status.Banned = true
			status.RetryAfter = rec.bannedUntil.Sub(now)
			return status
		}
		rec.bannedUntil = time.Time{}
		rec.failures = rec.failures[:0]
	}
	l.pruneLocked(rec, now)
	status.Failures = len(rec.failures)
	if status.Failures == 0 {
		l.removeLocked(elem)
	}
	return status
}

// TrackedSources reports how many source/scope pairs currently hold state.
// It exists so tests and operators can verify the memory bound.
func (l *Limiter) TrackedSources() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func (l *Limiter) getOrCreateLocked(scope Scope, addr netip.Addr, now time.Time) *record {
	key := limiterKey{scope: scope, addr: addr}
	if elem, ok := l.entries[key]; ok {
		l.lru.MoveToFront(elem)
		return elem.Value.(*record)
	}
	l.evictLocked(now)
	rec := &record{scope: scope, addr: addr}
	l.entries[key] = l.lru.PushFront(rec)
	return rec
}

// evictLocked keeps the population bounded. It prefers the least recently
// seen entry whose state has already expired, and otherwise evicts the least
// recently seen entry outright. Eviction can drop a ban under an extreme
// spray, which is why the thresholds, not this map, are the durable policy.
func (l *Limiter) evictLocked(now time.Time) {
	for len(l.entries) >= l.cfg.MaxSources {
		victim := l.lru.Back()
		if victim == nil {
			return
		}
		// Scan a bounded suffix of the LRU list for an expired entry so a
		// pile of active bans is not displaced unnecessarily.
		scanned := 0
		for elem := l.lru.Back(); elem != nil && scanned < 64; elem = elem.Prev() {
			scanned++
			rec := elem.Value.(*record)
			if !rec.bannedUntil.After(now) {
				victim = elem
				break
			}
		}
		l.removeLocked(victim)
	}
}

func (l *Limiter) pruneLocked(rec *record, now time.Time) {
	cutoff := now.Add(-l.cfg.Window)
	kept := rec.failures[:0]
	for _, ts := range rec.failures {
		if !ts.Before(cutoff) {
			kept = append(kept, ts)
		}
	}
	rec.failures = kept
}

func (l *Limiter) removeLocked(elem *list.Element) {
	rec := elem.Value.(*record)
	delete(l.entries, limiterKey{scope: rec.scope, addr: rec.addr})
	l.lru.Remove(elem)
}
