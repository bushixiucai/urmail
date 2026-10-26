package mailboxschedule

import (
	"testing"
	"time"
)

func TestNextAttemptBackoff(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	got := NextAttempt(1, now)
	if got.Sub(now) != 30*time.Second {
		t.Fatalf("attempt1=%v", got.Sub(now))
	}
	got = NextAttempt(3, now)
	if got.Sub(now) != 2*time.Minute {
		t.Fatalf("attempt3=%v", got.Sub(now))
	}
	got = NextAttempt(20, now)
	if got.Sub(now) != time.Hour {
		t.Fatalf("capped=%v", got.Sub(now))
	}
}

func TestShouldGiveUp(t *testing.T) {
	if ShouldGiveUp(MaxAttempts - 1) {
		t.Fatal("should retry")
	}
	if !ShouldGiveUp(MaxAttempts) {
		t.Fatal("should give up")
	}
}
