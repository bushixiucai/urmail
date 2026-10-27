package ai

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBreakerOpensAfterConsecutiveRecoverableFailures(t *testing.T) {
	b := NewBreaker(BreakerConfig{Threshold: 3, Cooldown: time.Minute})
	for range 3 {
		if err := b.Allow(); err != nil {
			t.Fatal("closed breaker rejected", err)
		}
		b.Record(ErrProviderUnavailable)
	}
	if b.State() != BreakerOpen {
		t.Fatal("breaker did not open after threshold failures")
	}
	if err := b.Allow(); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("open breaker admitted call: %v", err)
	}
}

func TestBreakerFatalFailuresDoNotOpen(t *testing.T) {
	b := NewBreaker(BreakerConfig{Threshold: 2, Cooldown: time.Minute})
	for range 10 {
		if err := b.Allow(); err != nil {
			t.Fatal(err)
		}
		b.Record(ErrInvalidRequest)
	}
	if b.State() != BreakerClosed {
		t.Fatal("fatal errors must not trip the circuit")
	}
}

func TestBreakerHalfOpenProbeClosesOnSuccess(t *testing.T) {
	b := NewBreaker(BreakerConfig{Threshold: 1, Cooldown: time.Minute})
	_ = b.Allow()
	b.Record(ErrRateLimitExceeded)
	if b.State() != BreakerOpen {
		t.Fatal("expected open")
	}
	b.nowFn = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if err := b.Allow(); err != nil {
		t.Fatal("half-open probe rejected", err)
	}
	if b.State() != BreakerHalfOpen {
		t.Fatalf("expected half_open, got %v", b.State())
	}
	// Concurrent callers are rejected while the probe runs.
	if err := b.Allow(); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("second half-open call admitted")
	}
	b.Record(nil)
	if b.State() != BreakerClosed {
		t.Fatal("successful probe must close the circuit")
	}
}

func TestBreakerHalfOpenProbeFailureReopens(t *testing.T) {
	b := NewBreaker(BreakerConfig{Threshold: 1, Cooldown: time.Minute})
	_ = b.Allow()
	b.Record(ErrRateLimitExceeded)
	b.nowFn = func() time.Time { return time.Now().Add(2 * time.Minute) }
	_ = b.Allow()
	b.Record(ErrProviderUnavailable)
	if b.State() != BreakerOpen {
		t.Fatal("failed probe must reopen the circuit")
	}
	if err := b.Allow(); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("reopened breaker must reject until cooldown")
	}
}

func TestBreakerProviderBlocksWhenOpenAndRecords(t *testing.T) {
	inner := &fakeBreakerProvider{err: ErrRateLimitExceeded}
	provider := &BreakerProvider{Provider: inner, breaker: NewBreaker(BreakerConfig{Threshold: 1, Cooldown: time.Minute}), kind: "test", provider: "test"}
	if _, err := provider.Complete(context.Background(), CompletionRequest{Prompt: "x"}); err == nil {
		t.Fatal("expected provider error")
	}
	if _, err := provider.Complete(context.Background(), CompletionRequest{Prompt: "x"}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("second call should hit the open circuit, got %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("open circuit still reached provider: %d calls", inner.calls)
	}
}

type fakeBreakerProvider struct {
	err   error
	calls int
}

func (f *fakeBreakerProvider) Name() string { return "test" }
func (f *fakeBreakerProvider) Complete(context.Context, CompletionRequest) (CompletionResponse, error) {
	f.calls++
	return CompletionResponse{}, f.err
}
