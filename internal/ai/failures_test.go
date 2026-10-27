package ai

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestClassifyFailureBoundedClasses(t *testing.T) {
	cases := map[error]FailureClass{
		nil:                  ClassFatal,
		ErrRateLimitExceeded: ClassRateLimited,
		fmt.Errorf("wrap: %w", ErrRateLimitExceeded):   ClassRateLimited,
		ErrProviderUnavailable:                         ClassUnavailable,
		context.DeadlineExceeded:                       ClassUnavailable,
		fmt.Errorf("status 500: %w", ErrProviderError): ClassUnavailable,
		ErrInvalidRequest:                              ClassFatal,
		ErrInvalidResponse:                             ClassFatal,
		ErrUnauthorized:                                ClassFatal,
		errors.New("unknown"):                          ClassFatal,
	}
	for err, want := range cases {
		if got := ClassifyFailure(err); got != want {
			t.Fatalf("ClassifyFailure(%v) = %d, want %d", err, got, want)
		}
	}
}

func TestRetryIdempotentRetriesTransientAndStops(t *testing.T) {
	sleeps := 0
	cfg := RetryConfig{Attempts: 3, Base: time.Millisecond, Sleep: func(context.Context, time.Duration) error { sleeps++; return nil }}
	attempts := 0
	err := RetryIdempotent(context.Background(), cfg, func() error {
		attempts++
		if attempts < 3 {
			return ErrProviderUnavailable
		}
		return nil
	})
	if err != nil || attempts != 3 || sleeps != 2 {
		t.Fatalf("err=%v attempts=%d sleeps=%d", err, attempts, sleeps)
	}
}

func TestRetryIdempotentDoesNotRetryFatalOrBeyondCap(t *testing.T) {
	cfg := RetryConfig{Attempts: 5, Base: time.Millisecond, Sleep: func(context.Context, time.Duration) error { return nil }}
	attempts := 0
	if err := RetryIdempotent(context.Background(), cfg, func() error { attempts++; return ErrInvalidRequest }); err == nil || attempts != 1 {
		t.Fatalf("fatal retried: attempts=%d", attempts)
	}
	attempts = 0
	if err := RetryIdempotent(context.Background(), cfg, func() error { attempts++; return ErrRateLimitExceeded }); err == nil || attempts != 5 {
		t.Fatalf("cap not enforced: attempts=%d", attempts)
	}
}

func TestRetryIdempotentCappedBackoff(t *testing.T) {
	var delays []time.Duration
	cfg := RetryConfig{Attempts: 5, Base: 100 * time.Millisecond, Max: 250 * time.Millisecond, Sleep: func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }}
	_ = RetryIdempotent(context.Background(), cfg, func() error { return ErrProviderUnavailable })
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond}
	if len(delays) != len(want) {
		t.Fatalf("delays=%v", delays)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("delays=%v want %v", delays, want)
		}
	}
}
