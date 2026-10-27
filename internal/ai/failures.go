package ai

import (
	"context"
	"errors"
	"time"
)

// ErrProviderUnavailable marks a provider that cannot serve requests right now
// (5xx, network failure, timeout, or an open circuit). Distinct from
// ErrRateLimitExceeded so clients can act differently on each.
var ErrProviderUnavailable = errors.New("ai provider is unavailable")

// FailureClass is a bounded classification of provider failures.
type FailureClass int

// Failure classifications.
const (
	// ClassFatal errors must never be retried: invalid request, bad response,
	// authorization, local-only policy.
	ClassFatal FailureClass = iota
	// ClassRateLimited is an explicit upstream 429; retry only for idempotent work.
	ClassRateLimited
	// ClassUnavailable covers timeouts, network errors, 5xx and open circuits.
	ClassUnavailable
)

// ClassifyFailure maps an error returned anywhere in the provider stack to its
// bounded class. Unknown errors are fatal: only explicit classifications
// become retryable.
func ClassifyFailure(err error) FailureClass {
	switch {
	case err == nil:
		return ClassFatal
	case errors.Is(err, ErrRateLimitExceeded):
		return ClassRateLimited
	case errors.Is(err, ErrProviderUnavailable):
		return ClassUnavailable
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return ClassUnavailable
	case errors.Is(err, ErrProviderError):
		// The adapters fold upstream 5xx bodies into ErrProviderError; treat
		// the transport-level envelope as unavailable (never retry the
		// generation itself, only idempotent callers consult the class).
		return ClassUnavailable
	default:
		return ClassFatal
	}
}

// RetryConfig bounds an idempotent retry loop.
type RetryConfig struct {
	Attempts int           // total attempts including the first; <=1 means one try
	Base     time.Duration // first backoff; doubles per attempt
	Max      time.Duration // backoff ceiling
	// RetryIf overrides the default ClassifyFailure-based decision when set.
	RetryIf func(err error) bool
	Sleep   func(context.Context, time.Duration) error // defaults to timer+ctx; injectable in tests
}

// RetryIdempotent runs fn up to cfg.Attempts times, retrying only classified
// transient failures with capped exponential backoff. Generation calls must
// never use this helper: users retry those themselves.
func RetryIdempotent(ctx context.Context, cfg RetryConfig, fn func() error) error {
	if cfg.Attempts < 1 {
		cfg.Attempts = 1
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	var err error
	delay := cfg.Base
	for attempt := 1; ; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if attempt >= cfg.Attempts {
			return err
		}
		if cfg.RetryIf != nil {
			if !cfg.RetryIf(err) {
				return err
			}
		} else if ClassifyFailure(err) == ClassFatal {
			return err
		}
		if delay <= 0 {
			delay = 100 * time.Millisecond
		}
		if cfg.Max > 0 && delay > cfg.Max {
			delay = cfg.Max
		}
		if sleepErr := sleep(ctx, delay); sleepErr != nil {
			return sleepErr
		}
		if cfg.Max > 0 {
			delay *= 2
			if delay > cfg.Max {
				delay = cfg.Max
			}
		} else {
			delay *= 2
		}
	}
}
