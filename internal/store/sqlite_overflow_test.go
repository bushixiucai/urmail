package store

import (
	"math"
	"testing"
)

// TestReportVersionRejectsOutOfRange pins the G115 guard: a ledger version that
// does not fit int32 must fail the read instead of being truncated.
func TestReportVersionRejectsOutOfRange(t *testing.T) {
	for _, bad := range []int64{-1, math.MaxInt32 + 1, math.MinInt64} {
		if _, err := reportVersion(bad); err == nil {
			t.Fatalf("reportVersion(%d) accepted an out-of-range value", bad)
		}
	}
	for _, good := range []int64{0, 1, math.MaxInt32} {
		got, err := reportVersion(good)
		if err != nil || int64(got) != good {
			t.Fatalf("reportVersion(%d) = %d, %v", good, got, err)
		}
	}
}

// TestMarketingRetryBackoffClamps pins the shift guard: negative attempts fall
// back to the first retry and attempts beyond ten are capped, so the backoff can
// never overflow or shift by a negative/oversized count.
func TestMarketingRetryBackoffClamps(t *testing.T) {
	cases := map[int64]int64{-5: 1, 0: 1, 1: 2, 10: 1024, 11: 1024, 1 << 40: 1024}
	for attempts, want := range cases {
		if got := marketingRetryBackoffMicros(attempts); got != want {
			t.Fatalf("backoff(%d) = %d, want %d", attempts, got, want)
		}
	}
}
