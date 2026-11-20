package store

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// TestMarketingNextBoundary pins the SQLite port of PG's
// (date_trunc('day', t AT TIME ZONE tz) + interval '1 day') AT TIME ZONE tz to
// the next local midnight — including the DST days where a local day is 23 or
// 25 hours, which is exactly the case the quota-window rollover cares about.
func TestMarketingNextBoundary(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	cases := []struct {
		name string
		at   time.Time
	}{
		{"spring forward", time.Date(2026, 3, 8, 12, 0, 0, 0, ny)},
		{"fall back", time.Date(2026, 11, 1, 12, 0, 0, 0, ny)},
		{"plain day", time.Date(2026, 7, 4, 23, 59, 59, 999999000, ny)},
	}
	for _, tc := range cases {
		got := marketingNextBoundary(tc.at.UnixMicro(), ny)
		want := time.Date(tc.at.Year(), tc.at.Month(), tc.at.Day()+1, 0, 0, 0, 0, ny).UnixMicro()
		if got != want {
			t.Errorf("%s: next boundary = %s, want %s",
				tc.name, time.UnixMicro(got).In(ny), time.UnixMicro(want).In(ny))
		}
		if delta := got - tc.at.UnixMicro(); delta <= 0 || delta > 25*3600*int64(time.Second/time.Microsecond) {
			t.Errorf("%s: boundary delta %d outside a 24h day", tc.name, delta)
		}
	}
}

// TestLeaseStale pins PG's
// `lease_owner IS DISTINCT FROM p_owner OR lease_fence<>p_fence
//
//	OR lease_until IS NULL OR lease_until<=now()`.
func TestLeaseStale(t *testing.T) {
	now := int64(1_000_000)
	owner := "worker-a"
	live := now + 1
	expired := now

	cases := []struct {
		name        string
		row         marketingFlowPositionRow
		owner       string
		fence       int64
		checkExpiry bool
		want        bool
	}{
		{"matching lease", marketingFlowPositionRow{LeaseOwner: &owner, LeaseFence: 7, LeaseUntil: &live}, owner, 7, true, false},
		{"nil owner", marketingFlowPositionRow{LeaseFence: 7, LeaseUntil: &live}, owner, 7, true, true},
		{"other owner", marketingFlowPositionRow{LeaseOwner: strptr("worker-b"), LeaseFence: 7, LeaseUntil: &live}, owner, 7, true, true},
		{"other fence", marketingFlowPositionRow{LeaseOwner: &owner, LeaseFence: 6, LeaseUntil: &live}, owner, 7, true, true},
		{"nil expiry", marketingFlowPositionRow{LeaseOwner: &owner, LeaseFence: 7}, owner, 7, true, true},
		{"expired", marketingFlowPositionRow{LeaseOwner: &owner, LeaseFence: 7, LeaseUntil: &expired}, owner, 7, true, true},
		{"no expiry check", marketingFlowPositionRow{LeaseOwner: &owner, LeaseFence: 7}, owner, 7, false, false},
	}
	for _, tc := range cases {
		if got := leaseStale(tc.row, tc.owner, tc.fence, now, tc.checkExpiry); got != tc.want {
			t.Errorf("%s: leaseStale = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func strptr(s string) *string { return &s }

// TestMarketingLimitPatterns pins urmail_marketing_limits' regex gates.
func TestMarketingLimitPatterns(t *testing.T) {
	for _, v := range []string{"1", "3", "100", "999999999"} {
		if !marketingDayLimitPattern.MatchString(v) {
			t.Errorf("day limit %q must be accepted", v)
		}
	}
	for _, v := range []string{"0", "01", "-1", "1000000000", "3.5", " 3", "abc"} {
		if marketingDayLimitPattern.MatchString(v) {
			t.Errorf("day limit %q must be rejected", v)
		}
	}
	for _, v := range []string{"1", "60", "999999"} {
		if !marketingRateLimitPattern.MatchString(v) {
			t.Errorf("rate limit %q must be accepted", v)
		}
	}
	for _, v := range []string{"0", "1000000", "60.0", "1e3"} {
		if marketingRateLimitPattern.MatchString(v) {
			t.Errorf("rate limit %q must be rejected", v)
		}
	}
}

// TestDNSReasonLength pins PG's length(trim(reason)) semantics: characters, not
// bytes, so a short CJK reason stays rejected.
func TestDNSReasonLength(t *testing.T) {
	if dnsReasonOK("         ") {
		t.Error("a blank reason must be rejected")
	}
	if dnsReasonOK("太短了") {
		t.Error("three characters must be rejected")
	}
	if !dnsReasonOK("这是一个足够长的理由") {
		t.Error("ten characters must be accepted")
	}
	if !dnsReasonOK(" 1234567890 ") {
		t.Error("trimmed ten characters must be accepted")
	}
}

// TestDNSConflictFree pins jsonb_array_length(changes->'conflicts')=0.
func TestDNSConflictFree(t *testing.T) {
	cases := map[string]bool{
		`{}`:                           true,
		`{"conflicts":[]}`:             true,
		`{"conflicts":[{"c":"x"}]}`:    false,
		`{"conflicts":null}`:           false,
		`{"conflicts":"not-an-array"}`: false,
		`not json`:                     false,
	}
	for in, want := range cases {
		if got := dnsConflictFree(in); got != want {
			t.Errorf("dnsConflictFree(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestUUIDText pins the PG text cast of a uuid for SQLite's TEXT columns.
func TestUUIDText(t *testing.T) {
	u := pgtype.UUID{Valid: true}
	for i := range u.Bytes {
		u.Bytes[i] = byte(i)
	}
	got, ok := uuidText(u)
	if !ok {
		t.Fatal("valid uuid reported invalid")
	}
	const want = "00010203-0405-0607-0809-0a0b0c0d0e0f"
	if got != want {
		t.Errorf("uuidText = %q, want %q", got, want)
	}
	if _, ok := uuidText(pgtype.UUID{}); ok {
		t.Error("invalid uuid must not render")
	}
}

// TestTimestamptzMicros pins the timestamp conversion used by the schedule path.
func TestTimestamptzMicros(t *testing.T) {
	ts := pgtype.Timestamptz{Time: time.Date(2026, 11, 20, 12, 0, 0, 123456000, time.UTC), Valid: true}
	got, ok := timestamptzMicros(ts)
	if !ok || got != ts.Time.UnixMicro() {
		t.Errorf("timestamptzMicros = (%d,%v), want %d", got, ok, ts.Time.UnixMicro())
	}
	if _, ok := timestamptzMicros(pgtype.Timestamptz{}); ok {
		t.Error("NULL timestamptz must report invalid")
	}
}
