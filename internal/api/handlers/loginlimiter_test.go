package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
)

func TestLoginLimiterBlocksAfterMaxFailures(t *testing.T) {
	l := newLoginLimiter()
	key := "1.2.3.4|user@example.com"

	for i := 0; i < loginMaxFailures; i++ {
		if !l.allow(key) {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
		l.fail(key)
	}
	if l.allow(key) {
		t.Fatalf("attempt %d must be blocked", loginMaxFailures+1)
	}
	// A different identity is unaffected.
	if !l.allow("5.6.7.8|other@example.com") {
		t.Fatalf("unrelated identity must not be blocked")
	}
}

func TestLoginLimiterResetAfterSuccess(t *testing.T) {
	l := newLoginLimiter()
	key := "1.2.3.4|user@example.com"
	for i := 0; i < loginMaxFailures; i++ {
		l.fail(key)
	}
	l.reset(key)
	if !l.allow(key) {
		t.Fatalf("after reset the identity must be allowed again")
	}
}

func TestHashSessionTokenIsStableAndIrreversible(t *testing.T) {
	raw := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	h1 := middleware.HashSessionToken(raw)
	h2 := middleware.HashSessionToken(raw)
	if h1 != h2 {
		t.Fatalf("hash must be deterministic")
	}
	if h1 == raw {
		t.Fatalf("hash must not equal the raw token")
	}
	if len(h1) != 64 {
		t.Fatalf("sha256 hex must be 64 chars, got %d", len(h1))
	}
	if middleware.HashSessionToken("other") == h1 {
		t.Fatalf("different tokens must hash differently")
	}
}

func TestDummyPasswordHashIsUsable(t *testing.T) {
	if dummyPasswordHash == "" {
		t.Fatalf("dummy hash must be generated at init")
	}
	if !panelpassword.Verify("urmail-dummy-password-for-timing", dummyPasswordHash) {
		t.Fatalf("dummy hash must verify against its own password")
	}
}

// TestLoginLimiterSprayBypassDefeated verifies that an attacker cannot spray 49
// accounts, log into their own account to reset the IP failure count, and bypass
// the 50-failure IP rate limit.
func TestLoginLimiterSprayBypassDefeated(t *testing.T) {
	l := newLoginLimiter()
	ip := "198.51.100.1"
	ipKey := "ip|" + ip

	// Step 1: Attacker sends 49 failed login attempts to 49 distinct email accounts.
	for i := 1; i <= 49; i++ {
		identityKey := fmt.Sprintf("%s|victim%d@example.com", ip, i)
		res, err := l.BeginAttempt(identityKey, ipKey, DefaultLoginBudgets)
		if err != nil {
			t.Fatalf("probe %d from ip must be allowed under IP budget of 50, got err: %v", i, err)
		}
		res.RecordFailure()
	}

	// Verify IP currently has 49 failures.
	l.mu.Lock()
	if ipEntry := l.entries[ipKey]; ipEntry == nil || len(ipEntry.failures) != 49 {
		t.Fatalf("expected 49 failures on IP entry, got: %v", ipEntry)
	}
	l.mu.Unlock()

	// Step 2: Attacker logs into their own legitimate account from the same IP.
	ownIdentity := ip + "|attacker@own.com"
	res, err := l.BeginAttempt(ownIdentity, ipKey, DefaultLoginBudgets)
	if err != nil {
		t.Fatalf("legitimate login attempt should be allowed, got err: %v", err)
	}
	res.CompleteSuccess()

	// CRITICAL ASSERTION: Successful login must NOT clear the shared IP failure count!
	l.mu.Lock()
	ipEntry := l.entries[ipKey]
	if ipEntry == nil || len(ipEntry.failures) != 49 {
		t.Fatalf("CRITICAL SECURITY FLAW: shared IP failures cleared by successful login! failures=%v", ipEntry)
	}
	// The own account failure count is 0 / cleared.
	if ownEntry := l.entries[ownIdentity]; ownEntry != nil && len(ownEntry.failures) > 0 {
		t.Fatalf("expected own identity failure history to be cleared, got %v", ownEntry.failures)
	}
	l.mu.Unlock()

	// Step 3: Attacker makes the 50th probe (to victim50@example.com).
	probe50Identity := ip + "|victim50@example.com"
	res50, err := l.BeginAttempt(probe50Identity, ipKey, DefaultLoginBudgets)
	if err != nil {
		t.Fatalf("50th attempt from IP should still be allowed, got err: %v", err)
	}
	res50.RecordFailure()

	// Step 4: Attacker makes the 51st probe (to victim51@example.com).
	// Identity has 0 previous failures, but IP budget of 50 is exhausted.
	probe51Identity := ip + "|victim51@example.com"
	_, err51 := l.BeginAttempt(probe51Identity, ipKey, DefaultLoginBudgets)
	if !errors.Is(err51, ErrRateLimited) {
		t.Fatalf("51st probe MUST be blocked by ErrRateLimited, got: %v", err51)
	}

	// Verify that an innocent client from another IP is NOT blocked.
	otherIP := "203.0.113.2"
	otherIPKey := "ip|" + otherIP
	otherIdentity := otherIP + "|victim51@example.com"
	resOther, errOther := l.BeginAttempt(otherIdentity, otherIPKey, DefaultLoginBudgets)
	if errOther != nil {
		t.Fatalf("unrelated IP must not be rate limited, got err: %v", errOther)
	}
	resOther.Discard()
}

// TestLoginLimiterSprayBypassDefeated_EndToEndHTTP verifies the full HTTP auth pipeline:
// 49 probes -> 401, 1 own login -> 200, 50th probe -> 401, 51st probe -> 429.
func TestLoginLimiterSprayBypassDefeated_EndToEndHTTP(t *testing.T) {
	origLoginAttempts := loginAttempts
	loginAttempts = newLoginLimiter()
	t.Cleanup(func() { loginAttempts = origLoginAttempts })

	st := newFakeAuthStore()
	st.addUser(t, "attacker@own.com", false, "", nil) // password is "correct-horse"
	r := newMFATestRouter(st)

	attackerIP := "198.51.100.1"

	postLogin := func(ip, email, password string) (int, map[string]any) {
		body, _ := json.Marshal(map[string]string{"email": email, "password": password})
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/auth/login", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", ip)
		req.RemoteAddr = ip + ":12345"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// 1. Send 49 invalid login probes across 49 distinct victim accounts from attackerIP.
	for i := 1; i <= 49; i++ {
		email := fmt.Sprintf("victim%d@example.com", i)
		code, _ := postLogin(attackerIP, email, "wrong-password")
		if code != http.StatusUnauthorized {
			t.Fatalf("probe %d: expected 401, got %d", i, code)
		}
	}

	// 2. Successful login with attacker's own credentials.
	code, body := postLogin(attackerIP, "attacker@own.com", "correct-horse")
	if code != http.StatusOK {
		t.Fatalf("legitimate login: expected 200, got %d body %v", code, body)
	}

	// 3. 50th probe (to victim50@example.com) fails with 401.
	code, _ = postLogin(attackerIP, "victim50@example.com", "wrong-password")
	if code != http.StatusUnauthorized {
		t.Fatalf("probe 50: expected 401, got %d", code)
	}

	// 4. 51st probe MUST be intercepted with HTTP 429 Too Many Requests (AUTH_RATE_LIMITED).
	code, body = postLogin(attackerIP, "victim51@example.com", "wrong-password")
	if code != http.StatusTooManyRequests {
		t.Fatalf("CRITICAL: 51st probe must be 429, got %d body %v", code, body)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok || errObj["code"] != "AUTH_RATE_LIMITED" {
		t.Fatalf("expected AUTH_RATE_LIMITED, got %v", body)
	}

	// 5. Another IP targeting victim51@example.com is NOT rate-limited (returns 401).
	code, _ = postLogin("203.0.113.2", "victim51@example.com", "wrong-password")
	if code != http.StatusUnauthorized {
		t.Fatalf("different IP: expected 401, got %d", code)
	}
}

// TestLoginLimiterConcurrentBeginAttempt tests thread safety and exact reservation
// bounds under high concurrency.
func TestLoginLimiterConcurrentBeginAttempt(t *testing.T) {
	l := newLoginLimiter()
	ipKey := "ip|192.0.2.1"
	budget := LimiterBudgets{
		IdentityMaxFailures: 5,
		IPMaxFailures:       50,
		Window:              15 * time.Minute,
	}

	const goroutines = 100
	var granted int64
	var rejected int64
	var reservations []Reservation
	var mu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		email := fmt.Sprintf("concurrent%d@example.com", i)
		identityKey := fmt.Sprintf("192.0.2.1|%s", email)
		go func() {
			defer wg.Done()
			res, err := l.BeginAttempt(identityKey, ipKey, budget)
			if err == nil {
				atomic.AddInt64(&granted, 1)
				mu.Lock()
				reservations = append(reservations, res)
				mu.Unlock()
			} else if errors.Is(err, ErrRateLimited) {
				atomic.AddInt64(&rejected, 1)
			}
		}()
	}
	wg.Wait()

	if granted != 50 {
		t.Fatalf("expected exactly 50 granted reservations, got %d", granted)
	}
	if rejected != 50 {
		t.Fatalf("expected exactly 50 rejected reservations, got %d", rejected)
	}

	// Now settle all 50 reservations as failures
	for _, r := range reservations {
		r.RecordFailure()
	}

	// Subsequent attempts must still be blocked
	_, err := l.BeginAttempt("192.0.2.1|next@example.com", ipKey, budget)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("subsequent attempt after 50 failures must be rate limited, got %v", err)
	}
}

// TestLoginLimiterCapacityEvictionLRU verifies fail-closed behavior:
// when capacity is reached and all entries have unexpired failure records,
// the limiter MUST NOT evict unexpired entries; new attempts must fail-closed.
// Once time passes the sliding window, expired entries are reclaimed.
func TestLoginLimiterCapacityEvictionLRU(t *testing.T) {
	const maxCap = 20
	l := newLoginLimiterWithCapacity(maxCap)

	mockNow := time.Now()
	l.nowFunc = func() time.Time { return mockNow }

	// Step 1: Fill capacity with 20 distinct active failures
	for i := 1; i <= maxCap; i++ {
		key := fmt.Sprintf("active-key-%d", i)
		l.fail(key)
	}

	l.mu.Lock()
	if len(l.entries) != maxCap {
		t.Fatalf("expected %d entries, got %d", maxCap, len(l.entries))
	}
	l.mu.Unlock()

	// Step 2: Attempt to add a 21st entry while all 20 entries have UNEXPIRED failures.
	// Safety Requirement: Must FAIL-CLOSED! Must NOT evict active failure records!
	_, err := l.BeginAttempt("new-key", "ip|new-key", DefaultLoginBudgets)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited when capacity is full of active failures, got: %v", err)
	}

	// Verify all 20 original unexpired failure entries remain intact
	l.mu.Lock()
	for i := 1; i <= maxCap; i++ {
		key := fmt.Sprintf("active-key-%d", i)
		if entry := l.entries[key]; entry == nil || len(entry.failures) != 1 {
			t.Fatalf("active failure entry %s must NOT be evicted while unexpired!", key)
		}
	}
	l.mu.Unlock()

	// Step 3: Advance time past sliding window (15m) so the entries expire
	mockNow = mockNow.Add(16 * time.Minute)

	// Now a new attempt should be able to reclaim expired entries and succeed
	res, err := l.BeginAttempt("reclaimed-key", "ip|reclaimed-key", DefaultLoginBudgets)
	if err != nil {
		t.Fatalf("expected successful BeginAttempt after window expiry, got err: %v", err)
	}
	res.RecordFailure()

	l.mu.Lock()
	if len(l.entries) > maxCap {
		t.Fatalf("capacity exceeded: %d > %d", len(l.entries), maxCap)
	}
	l.mu.Unlock()
}

// TestLoginLimiterAllPendingCapacityBoundary verifies that when all entries in
// the table are in-flight (pending > 0), the limiter fails-closed and NEVER
// evicts an in-flight reservation.
func TestLoginLimiterAllPendingCapacityBoundary(t *testing.T) {
	const maxCap = 10
	l := newLoginLimiterWithCapacity(maxCap)

	// Hold 10 reservations (single key each for simplicity)
	var held []Reservation
	for i := 1; i <= maxCap; i++ {
		key := fmt.Sprintf("inflight-%d", i)
		res, err := l.BeginAttempt(key, "", DefaultLoginBudgets)
		if err != nil {
			t.Fatalf("reservation %d should succeed within capacity: %v", i, err)
		}
		held = append(held, res)
	}

	l.mu.Lock()
	if len(l.entries) != maxCap {
		t.Fatalf("expected %d entries, got %d", maxCap, len(l.entries))
	}
	for i := 1; i <= maxCap; i++ {
		key := fmt.Sprintf("inflight-%d", i)
		if e := l.entries[key]; e == nil || e.pending != 1 {
			t.Fatalf("entry %s must have pending=1, got %v", key, e)
		}
	}
	l.mu.Unlock()

	// Attempt #11 arrives: all 10 slots are in-flight.
	// Limiter MUST fail-closed: return ErrRateLimited without evicting any in-flight entry!
	_, err := l.BeginAttempt("inflight-11", "", DefaultLoginBudgets)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("attempt #11 must be rate limited fail-closed, got: %v", err)
	}

	// Verify all 10 in-flight reservations are still intact
	l.mu.Lock()
	for i := 1; i <= maxCap; i++ {
		key := fmt.Sprintf("inflight-%d", i)
		if e := l.entries[key]; e == nil || e.pending != 1 {
			t.Fatalf("in-flight entry %s was corrupted or evicted!", key)
		}
	}
	l.mu.Unlock()

	// Release 1 held reservation via Discard
	held[0].Discard()

	// Now slot is freed (entry deleted because failures=0 and pending=0)
	resNew, errNew := l.BeginAttempt("inflight-new", "", DefaultLoginBudgets)
	if errNew != nil {
		t.Fatalf("attempt after release should succeed, got %v", errNew)
	}
	resNew.Discard()
}

// TestLoginLimiterDualKeyAtomicityAtCapacityBoundary verifies that BeginAttempt
// guarantees atomic capacity for BOTH identityKey and ipKey without partial mutation.
func TestLoginLimiterDualKeyAtomicityAtCapacityBoundary(t *testing.T) {
	// Table capacity is only 1.
	l := newLoginLimiterWithCapacity(1)

	// An attempt with two distinct new keys (identity + ip) requires 2 slots.
	// Since 2 > maxCap(1), it must fail-closed and leave 0 entries in table!
	_, err := l.BeginAttempt("identity-user", "ip|1.2.3.4", DefaultLoginBudgets)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("dual key attempt with cap=1 must be rejected, got %v", err)
	}

	// CRITICAL ASSERTION: No partial mutation!
	l.mu.Lock()
	if len(l.entries) != 0 {
		t.Fatalf("CRITICAL FLAW: partial mutation left %d entries in table when overall reservation failed!", len(l.entries))
	}
	l.mu.Unlock()
}

// TestLoginLimiterReviewCounterexample_Cap4LockoutWashout tests the reviewer counterexample:
// cap4: A (idA, ipA) has 50 failures and is locked out. B and C each with distinct identity and IP
// fail. This must NOT wash away A's failure window!
func TestLoginLimiterReviewCounterexample_Cap4LockoutWashout(t *testing.T) {
	const maxCap = 4
	l := newLoginLimiterWithCapacity(maxCap)

	idA := "1.1.1.1|userA@example.com"
	ipA := "ip|1.1.1.1"

	// Lock out A with 50 failures on ipA (and 5 on idA)
	for i := 0; i < 50; i++ {
		res, err := l.BeginAttempt(idA, ipA, LimiterBudgets{IdentityMaxFailures: 50, IPMaxFailures: 50, Window: loginWindow})
		if err != nil {
			t.Fatalf("locking out A failed at attempt %d: %v", i+1, err)
		}
		res.RecordFailure()
	}

	// At this point, entries in table: idA and ipA (2 of 4 slots used).
	l.mu.Lock()
	if len(l.entries) != 2 {
		t.Fatalf("expected 2 entries (idA, ipA), got %d", len(l.entries))
	}
	if e := l.entries[ipA]; e == nil || len(e.failures) != 50 {
		t.Fatalf("expected 50 failures on ipA, got %v", e)
	}
	l.mu.Unlock()

	// User B makes an attempt and fails: takes slots 3 and 4 (idB, ipB).
	idB := "2.2.2.2|userB@example.com"
	ipB := "ip|2.2.2.2"
	resB, errB := l.BeginAttempt(idB, ipB, DefaultLoginBudgets)
	if errB != nil {
		t.Fatalf("attempt B should succeed within capacity: %v", errB)
	}
	resB.RecordFailure()

	// Now table is COMPLETELY FULL (4/4 slots): idA, ipA, idB, ipB.
	l.mu.Lock()
	if len(l.entries) != 4 {
		t.Fatalf("expected table to be full at 4, got %d", len(l.entries))
	}
	l.mu.Unlock()

	// Attacker tries attempt C with distinct idC and ipC to wash out A's failure window!
	idC := "3.3.3.3|userC@example.com"
	ipC := "ip|3.3.3.3"
	_, errC := l.BeginAttempt(idC, ipC, DefaultLoginBudgets)
	// MUST FAIL-CLOSED because capacity is full of active, unexpired failures!
	if !errors.Is(errC, ErrRateLimited) {
		t.Fatalf("attempt C must fail-closed when capacity is full of active failures, got: %v", errC)
	}

	// CRITICAL ASSERTION: A's failure window has NOT been washed away!
	l.mu.Lock()
	entryA := l.entries[ipA]
	if entryA == nil || len(entryA.failures) != 50 {
		t.Fatalf("CRITICAL SECURITY FLAW: A's failure window was washed away! entryA=%v", entryA)
	}
	l.mu.Unlock()

	// Attempt by A MUST STILL BE LOCKED OUT!
	_, errA := l.BeginAttempt(idA, ipA, DefaultLoginBudgets)
	if !errors.Is(errA, ErrRateLimited) {
		t.Fatalf("user A must STILL be locked out, got: %v", errA)
	}
}

// TestLoginLimiterReviewCounterexample_Cap2InflightAndABAGuard tests the reviewer counterexample:
// cap2: r1(idA,ipA) holds both slots. Attempt r2(idB,ipB) arrives while r1 is inflight.
// r2 must be rejected (no slots), and r1 must not be evicted.
// Also tests ABA protection: r1 calling Discard cannot clobber a subsequent reservation's pending.
func TestLoginLimiterReviewCounterexample_Cap2InflightAndABAGuard(t *testing.T) {
	const maxCap = 2
	l := newLoginLimiterWithCapacity(maxCap)

	idA := "1.1.1.1|userA@example.com"
	ipA := "ip|1.1.1.1"

	// r1 reserves idA and ipA (2 slots)
	r1, err1 := l.BeginAttempt(idA, ipA, DefaultLoginBudgets)
	if err1 != nil {
		t.Fatalf("r1 should succeed: %v", err1)
	}

	// Table is full (2/2) with both entries having pending=1.
	l.mu.Lock()
	if len(l.entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(l.entries))
	}
	l.mu.Unlock()

	// r2 arrives with idB and ipB. Needs 2 slots.
	idB := "2.2.2.2|userB@example.com"
	ipB := "ip|2.2.2.2"
	_, err2 := l.BeginAttempt(idB, ipB, DefaultLoginBudgets)
	// MUST FAIL-CLOSED without evicting r1!
	if !errors.Is(err2, ErrRateLimited) {
		t.Fatalf("r2 must be rejected when slots are full of inflight entries, got %v", err2)
	}

	// Verify r1's entries are intact and pending=1
	l.mu.Lock()
	if e := l.entries[idA]; e == nil || e.pending != 1 {
		t.Fatalf("idA was corrupted or evicted: %v", e)
	}
	if e := l.entries[ipA]; e == nil || e.pending != 1 {
		t.Fatalf("ipA was corrupted or evicted: %v", e)
	}
	l.mu.Unlock()

	// Test ABA protection:
	// Suppose r1 was discarded, freeing idA and ipA (entries removed because failures=0 and pending=0).
	r1.Discard()

	l.mu.Lock()
	if len(l.entries) != 0 {
		t.Fatalf("entries should be cleaned up after discard with 0 failures, got %d", len(l.entries))
	}
	l.mu.Unlock()

	// Now r3 arrives with idA and ipA. It gets freshly allocated entries with new entry IDs!
	r3, err3 := l.BeginAttempt(idA, ipA, DefaultLoginBudgets)
	if err3 != nil {
		t.Fatalf("r3 should succeed: %v", err3)
	}

	l.mu.Lock()
	if e := l.entries[idA]; e == nil || e.pending != 1 {
		t.Fatalf("r3 idA must have pending=1, got %v", e)
	}
	l.mu.Unlock()

	// If r1 were somehow to call Discard or RecordFailure again (e.g. stale pointer / ABA race),
	// it must NOT decrement r3's pending count!
	r1Typed := r1.(*limiterReservation)
	r1Typed.settled = false // simulate stale invocation bypass
	r1.Discard()

	// Verify r3's pending is STILL 1!
	l.mu.Lock()
	if e := l.entries[idA]; e == nil || e.pending != 1 {
		t.Fatalf("ABA FLAW: stale r1.Discard decremented r3's pending! entry=%v", e)
	}
	l.mu.Unlock()

	r3.Discard()
}

// TestLoginLimiterMultipleInflightOneSucceeds verifies that when multiple in-flight
// reservations exist for the same key, CompleteSuccess on one decrements its own pending,
// clears failure history, but does NOT clear or corrupt the pending count of other in-flight attempts.
func TestLoginLimiterMultipleInflightOneSucceeds(t *testing.T) {
	l := newLoginLimiter()
	idKey := "1.1.1.1|user@example.com"
	ipKey := "ip|1.1.1.1"

	// Pre-record 2 failures
	resPre, _ := l.BeginAttempt(idKey, ipKey, DefaultLoginBudgets)
	resPre.RecordFailure()
	resPre2, _ := l.BeginAttempt(idKey, ipKey, DefaultLoginBudgets)
	resPre2.RecordFailure()

	l.mu.Lock()
	if e := l.entries[idKey]; e == nil || len(e.failures) != 2 || e.pending != 0 {
		t.Fatalf("expected 2 failures, got %v", e)
	}
	l.mu.Unlock()

	// Launch 2 concurrent in-flight attempts
	r1, err1 := l.BeginAttempt(idKey, ipKey, DefaultLoginBudgets)
	if err1 != nil {
		t.Fatal(err1)
	}
	r2, err2 := l.BeginAttempt(idKey, ipKey, DefaultLoginBudgets)
	if err2 != nil {
		t.Fatal(err2)
	}

	l.mu.Lock()
	if e := l.entries[idKey]; e == nil || e.pending != 2 {
		t.Fatalf("expected pending=2 on idKey, got %v", e)
	}
	if e := l.entries[ipKey]; e == nil || e.pending != 2 {
		t.Fatalf("expected pending=2 on ipKey, got %v", e)
	}
	l.mu.Unlock()

	// r1 completes successfully!
	r1.CompleteSuccess()

	// Assertions after r1 succeeds:
	// - idKey failures cleared (0), pending decremented to 1 (r2 is still inflight!).
	// - ipKey failures PRESERVED (2), pending decremented to 1 (r2 is still inflight!).
	l.mu.Lock()
	entryID := l.entries[idKey]
	if entryID == nil || len(entryID.failures) != 0 || entryID.pending != 1 {
		t.Fatalf("expected idKey failures=0 and pending=1 after r1 success, got failures=%d pending=%d", len(entryID.failures), entryID.pending)
	}
	entryIP := l.entries[ipKey]
	if entryIP == nil || len(entryIP.failures) != 2 || entryIP.pending != 1 {
		t.Fatalf("expected ipKey failures=2 and pending=1 after r1 success, got failures=%d pending=%d", len(entryIP.failures), entryIP.pending)
	}
	l.mu.Unlock()

	// Now r2 fails
	r2.RecordFailure()

	// Assertions after r2 fails:
	// - idKey pending becomes 0, failures becomes 1 (new failure recorded!).
	// - ipKey pending becomes 0, failures becomes 3 (2 original + 1 new!).
	l.mu.Lock()
	entryID = l.entries[idKey]
	if entryID == nil || len(entryID.failures) != 1 || entryID.pending != 0 {
		t.Fatalf("expected idKey failures=1 and pending=0 after r2 failure, got failures=%d pending=%d", len(entryID.failures), entryID.pending)
	}
	entryIP = l.entries[ipKey]
	if entryIP == nil || len(entryIP.failures) != 3 || entryIP.pending != 0 {
		t.Fatalf("expected ipKey failures=3 and pending=0 after r2 failure, got failures=%d pending=%d", len(entryIP.failures), entryIP.pending)
	}
	l.mu.Unlock()
}

// TestLoginLimiterConcurrentNoOversell verifies that concurrent BeginAttempt calls
// never oversell capacity.
func TestLoginLimiterConcurrentNoOversell(t *testing.T) {
	const maxCap = 20
	l := newLoginLimiterWithCapacity(maxCap)

	const goroutines = 100
	var granted int64
	var rejected int64

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		key := fmt.Sprintf("concurrent-key-%d", i)
		go func() {
			defer wg.Done()
			res, err := l.BeginAttempt(key, "", DefaultLoginBudgets)
			if err == nil {
				atomic.AddInt64(&granted, 1)
				// Do not settle immediately, keep pending to hold capacity
				_ = res
			} else if errors.Is(err, ErrRateLimited) {
				atomic.AddInt64(&rejected, 1)
			}
		}()
	}
	wg.Wait()

	if granted != maxCap {
		t.Fatalf("expected exactly %d granted, got %d", maxCap, granted)
	}
	if rejected != goroutines-maxCap {
		t.Fatalf("expected exactly %d rejected, got %d", goroutines-maxCap, rejected)
	}

	l.mu.Lock()
	if len(l.entries) != maxCap {
		t.Fatalf("expected exactly %d entries in table, got %d", maxCap, len(l.entries))
	}
	l.mu.Unlock()
}

// TestLoginLimiterMFALifecycle verifies that first-leg MFA releases pending reservation
// via Discard() without clearing failure history, and second leg reserves independently.
func TestLoginLimiterMFALifecycle(t *testing.T) {
	l := newLoginLimiter()
	identityKey := "1.2.3.4|mfa-user@example.com"
	ipKey := "ip|1.2.3.4"

	// Pre-record 2 failures
	res1, _ := l.BeginAttempt(identityKey, ipKey, DefaultLoginBudgets)
	res1.RecordFailure()
	res2, _ := l.BeginAttempt(identityKey, ipKey, DefaultLoginBudgets)
	res2.RecordFailure()

	// Leg 1: Password succeeds, MFA token is issued, calls Discard()
	resLeg1, err := l.BeginAttempt(identityKey, ipKey, DefaultLoginBudgets)
	if err != nil {
		t.Fatalf("leg 1 begin attempt failed: %v", err)
	}
	resLeg1.Discard()

	// History must NOT be cleared (2 failures still remain)
	l.mu.Lock()
	if entry := l.entries[identityKey]; entry == nil || len(entry.failures) != 2 {
		t.Fatalf("leg 1 discard should retain 2 failures, got %v", entry)
	}
	// Pending must be 0
	if entry := l.entries[identityKey]; entry.pending != 0 {
		t.Fatalf("pending must be 0 after discard, got %d", entry.pending)
	}
	l.mu.Unlock()

	// Leg 2: Validates code. If invalid code:
	resLeg2Fail, _ := l.BeginAttempt(identityKey, ipKey, DefaultLoginBudgets)
	resLeg2Fail.RecordFailure()

	l.mu.Lock()
	if entry := l.entries[identityKey]; len(entry.failures) != 3 {
		t.Fatalf("leg 2 failure should increase failures to 3, got %d", len(entry.failures))
	}
	l.mu.Unlock()

	// Leg 2: Valid code -> CompleteSuccess clears identity, retains IP
	resLeg2Success, _ := l.BeginAttempt(identityKey, ipKey, DefaultLoginBudgets)
	resLeg2Success.CompleteSuccess()

	l.mu.Lock()
	if entry := l.entries[identityKey]; entry != nil && len(entry.failures) != 0 {
		t.Fatalf("identity failures should be cleared after CompleteSuccess")
	}
	if ipEntry := l.entries[ipKey]; ipEntry == nil || len(ipEntry.failures) != 3 {
		t.Fatalf("IP failures must be preserved (3 failures), got %v", ipEntry)
	}
	l.mu.Unlock()
}

// TestLoginLimiterReservationIdempotence verifies multiple calls to settle methods are no-ops.
func TestLoginLimiterReservationIdempotence(t *testing.T) {
	l := newLoginLimiter()
	res, err := l.BeginAttempt("k1", "ip|k1", DefaultLoginBudgets)
	if err != nil {
		t.Fatal(err)
	}

	// Call RecordFailure, then Discard, then CompleteSuccess
	res.RecordFailure()
	res.Discard()
	res.CompleteSuccess()

	l.mu.Lock()
	if entry := l.entries["k1"]; entry == nil || len(entry.failures) != 1 || entry.pending != 0 {
		t.Fatalf("only the first settlement call should take effect, got %v", entry)
	}
	l.mu.Unlock()
}
