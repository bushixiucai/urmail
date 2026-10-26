package mailboxschedule

import "time"

const (
	// MaxAttempts caps retries before a row is marked failed.
	MaxAttempts = 8
	// DefaultInterval is the in-process poller tick.
	DefaultInterval = 15 * time.Second
)

// NextAttempt returns when a failed attempt should be retried.
// Cap is one hour; base is 30s with exponential growth.
func NextAttempt(attempts int, now time.Time) time.Time {
	if attempts < 1 {
		attempts = 1
	}
	delay := 30 * time.Second
	for i := 1; i < attempts && delay < time.Hour; i++ {
		delay *= 2
		if delay > time.Hour {
			delay = time.Hour
			break
		}
	}
	return now.UTC().Add(delay)
}

// ShouldGiveUp reports whether attempts have exhausted the retry budget.
func ShouldGiveUp(attempts int) bool {
	return attempts >= MaxAttempts
}
