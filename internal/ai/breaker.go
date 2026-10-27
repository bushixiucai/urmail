package ai

import (
	"context"
	"sync"
	"time"
)

// BreakerState is the bounded circuit-breaker state.
type BreakerState int

// Breaker states.
const (
	BreakerClosed BreakerState = iota
	BreakerOpen
	BreakerHalfOpen
)

// String names the breaker state for metrics.
func (s BreakerState) String() string {
	switch s {
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}

// BreakerConfig controls one provider circuit.
type BreakerConfig struct {
	// Threshold consecutive recoverable failures open the circuit.
	Threshold int
	// Cooldown is how long the circuit stays open before a half-open probe.
	Cooldown time.Duration
}

// Breaker is a per-provider consecutive-failure circuit breaker. After
// Threshold consecutive recoverable failures it opens for Cooldown, then
// admits exactly one half-open probe; probe success closes it, probe failure
// reopens it.
type Breaker struct {
	mu        sync.Mutex
	cfg       BreakerConfig
	state     BreakerState
	failures  int
	openedAt  time.Time
	probing   bool
	nowFn     func() time.Time
	transName string
}

// NewBreaker applies the documented defaults for zero fields.
func NewBreaker(cfg BreakerConfig) *Breaker {
	if cfg.Threshold <= 0 {
		cfg.Threshold = 5
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 30 * time.Second
	}
	return &Breaker{cfg: cfg, nowFn: time.Now}
}

// Allow reports whether a call may proceed. In half-open state exactly one
// probe is admitted; everyone else is rejected until the probe settles.
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case BreakerClosed:
		return nil
	case BreakerOpen:
		if b.nowFn().Sub(b.openedAt) >= b.cfg.Cooldown {
			b.state = BreakerHalfOpen
			b.probing = true
			b.recordTransitionLocked("half_open")
			return nil
		}
		return ErrProviderUnavailable
	default: // half_open: exactly one probe in flight, everyone else waits.
		return ErrProviderUnavailable
	}
}

// Record settles the call outcome. Fatal errors (invalid request, bad
// response, policy refusals) never count against the circuit: only
// recoverable provider failures do.
func (b *Breaker) Record(err error) {
	class := ClassifyFailure(err)
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case err == nil:
		b.failures = 0
		b.probing = false
		if b.state != BreakerClosed {
			b.state = BreakerClosed
			b.recordTransitionLocked("closed")
		}
	case class == ClassFatal:
		b.probing = false
		if b.state == BreakerHalfOpen {
			// A probe with a fatal outcome neither proves recovery nor
			// failure; reopen the cooldown window for the next probe.
			b.state = BreakerOpen
			b.openedAt = b.nowFn()
		}
	default:
		b.probing = false
		b.failures++
		if b.state == BreakerHalfOpen || b.failures >= b.cfg.Threshold {
			if b.state != BreakerOpen {
				b.state = BreakerOpen
				b.openedAt = b.nowFn()
				b.recordTransitionLocked("open")
			}
		}
	}
}

// State returns the current state (for tests and diagnostics only).
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *Breaker) recordTransitionLocked(transition string) {
	if b.transName != "" {
		BreakerTransitionsTotal.WithLabelValues(b.transName, transition).Inc()
	}
}

// breakerProviders tracks one breaker per provider name for WithBreaker.
var breakerMu sync.Mutex
var breakers = map[string]*Breaker{}

// BreakerFor returns the process-wide breaker for a provider name, creating
// it with the given config on first use.
func BreakerFor(provider string, cfg BreakerConfig) *Breaker {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	if existing, ok := breakers[provider]; ok {
		return existing
	}
	breaker := NewBreaker(cfg)
	breaker.transName = provider
	breakers[provider] = breaker
	return breaker
}

// BreakerProvider wraps a Provider with the per-provider circuit breaker and
// records request outcome plus latency metrics. It is additive: the wrapped
// provider keeps its original behavior when the breaker is closed.
type BreakerProvider struct {
	Provider
	breaker  *Breaker
	kind     string
	provider string
}

// WithBreaker wraps provider with the shared breaker for its Name() and
// metric instrumentation. kind is a bounded request kind label ("generation",
// "citation", ...).
func WithBreaker(provider Provider, kind string) Provider {
	if provider == nil {
		return nil
	}
	name := provider.Name()
	if name == "" {
		name = "unknown"
	}
	return &BreakerProvider{Provider: provider, breaker: BreakerFor(name, BreakerConfig{}), kind: kind, provider: name}
}

// Complete applies the breaker around exactly one upstream call. Automatic
// retries are deliberately NOT performed here: generation calls are never
// retried by the system, the user retries them.
func (p *BreakerProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	if allowErr := p.breaker.Allow(); allowErr != nil {
		RecordOutcome(p.kind, "circuit_open")
		return CompletionResponse{}, allowErr
	}
	start := time.Now()
	resp, err := p.Provider.Complete(ctx, req)
	ProviderLatencySeconds.WithLabelValues(p.provider).Observe(time.Since(start).Seconds())
	outcome := "succeeded"
	switch {
	case err == nil:
	case ClassifyFailure(err) == ClassRateLimited:
		outcome = "rate_limited"
	case ClassifyFailure(err) == ClassUnavailable:
		outcome = "unavailable"
	default:
		outcome = "rejected"
	}
	RecordOutcome(p.kind, outcome)
	p.breaker.Record(err)
	return resp, err
}
