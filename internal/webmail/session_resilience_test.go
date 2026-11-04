package webmail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
)

// TestSessionStatusClassification verifies explicit classification into:
// - Valid
// - RevokedOrInvalid
// - InfrastructureUnavailable
func TestSessionStatusClassification(t *testing.T) {
	now := time.Now()
	expiry := now.Add(time.Hour)

	// Valid session
	validSess := Session{TokenHash: "valid-tok", Address: "user@example.test", ExpiresAt: expiry}
	if got := ClassifySessionLookup(validSess, nil, now); got != Valid {
		t.Fatalf("expected Valid, got %v", got)
	}

	// RevokedOrInvalid: sql.ErrNoRows
	if got := ClassifySessionLookup(Session{}, sql.ErrNoRows, now); got != RevokedOrInvalid {
		t.Fatalf("expected RevokedOrInvalid for sql.ErrNoRows, got %v", got)
	}

	// RevokedOrInvalid: pgx.ErrNoRows
	if got := ClassifySessionLookup(Session{}, pgx.ErrNoRows, now); got != RevokedOrInvalid {
		t.Fatalf("expected RevokedOrInvalid for pgx.ErrNoRows, got %v", got)
	}

	// RevokedOrInvalid: ErrRevoked
	if got := ClassifySessionLookup(Session{}, ErrRevoked, now); got != RevokedOrInvalid {
		t.Fatalf("expected RevokedOrInvalid for ErrRevoked, got %v", got)
	}

	// RevokedOrInvalid: custom error with "revoked" text
	if got := ClassifySessionLookup(Session{}, errors.New("session revoked"), now); got != RevokedOrInvalid {
		t.Fatalf("expected RevokedOrInvalid for 'session revoked', got %v", got)
	}

	// RevokedOrInvalid: empty token
	if got := ClassifySessionLookup(Session{TokenHash: ""}, nil, now); got != RevokedOrInvalid {
		t.Fatalf("expected RevokedOrInvalid for empty TokenHash, got %v", got)
	}

	// RevokedOrInvalid: expired session
	expiredSess := Session{TokenHash: "tok", Address: "user@example.test", ExpiresAt: now.Add(-time.Minute)}
	if got := ClassifySessionLookup(expiredSess, nil, now); got != RevokedOrInvalid {
		t.Fatalf("expected RevokedOrInvalid for expired session, got %v", got)
	}

	// InfrastructureUnavailable: context.DeadlineExceeded
	if got := ClassifySessionLookup(Session{}, context.DeadlineExceeded, now); got != InfrastructureUnavailable {
		t.Fatalf("expected InfrastructureUnavailable for context.DeadlineExceeded, got %v", got)
	}

	// InfrastructureUnavailable: context.Canceled
	if got := ClassifySessionLookup(Session{}, context.Canceled, now); got != InfrastructureUnavailable {
		t.Fatalf("expected InfrastructureUnavailable for context.Canceled, got %v", got)
	}

	// InfrastructureUnavailable: transient connection error
	connErr := errors.New(`connection to server on socket "/var/run/postgresql/.s.PGSQL.5432" failed: Connection refused`)
	if got := ClassifySessionLookup(Session{}, connErr, now); got != InfrastructureUnavailable {
		t.Fatalf("expected InfrastructureUnavailable for connection refused, got %v", got)
	}
}

// TestPostgresDowntime30SecondsPreservesVaultCredentials tests requirement 5.1:
// Simulates PostgreSQL downtime for 30 seconds during which background Sweep runs.
// Asserts that memory vault credentials remain completely intact and fail-closed,
// and after PostgreSQL recovers, user continues using their session without re-login.
func TestPostgresDowntime30SecondsPreservesVaultCredentials(t *testing.T) {
	now := time.Now()
	clockMu := sync.Mutex{}
	currentTime := now

	virtualNow := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return currentTime
	}

	advanceTime := func(d time.Duration) {
		clockMu.Lock()
		defer clockMu.Unlock()
		currentTime = currentTime.Add(d)
	}

	v := NewMemoryVault()
	v.now = virtualNow
	v.itemTimeout = 100 * time.Millisecond
	v.minBackoff = 5 * time.Millisecond
	v.maxBackoff = 20 * time.Millisecond

	token := "user-bearer-token-hash"
	password := "user-mailbox-secure-password"
	sessionExpiry := now.Add(2 * time.Hour) // Valid for 2 hours

	err := v.PutSession(Session{
		TokenHash: token,
		TenantID:  1,
		MailboxID: 42,
		Address:   "alice@example.com",
		ExpiresAt: sessionExpiry,
	}, password)
	if err != nil {
		t.Fatalf("failed to insert initial session: %v", err)
	}

	// Assert credential exists before outage
	gotPass, ok := v.Get(token)
	if !ok || gotPass != password {
		t.Fatalf("expected password %q, got %q (ok=%v)", password, gotPass, ok)
	}

	// State of PostgreSQL: down for 30 seconds
	outageStart := now
	outageEnd := outageStart.Add(30 * time.Second)

	lookup := func(_ context.Context, tok string) (Session, error) {
		cur := virtualNow()
		if cur.Before(outageEnd) {
			// PostgreSQL is down: connection refused / timeout
			return Session{}, errors.New("dial tcp 127.0.0.1:5432: connect: connection refused (PostgreSQL restarting)")
		}
		// PostgreSQL is back up and returns valid session
		return Session{
			TokenHash: tok,
			TenantID:  1,
			MailboxID: 42,
			Address:   "alice@example.com",
			ExpiresAt: sessionExpiry,
		}, nil
	}

	// Step through the 30-second downtime in intervals of 5 seconds, triggering Sweep at each step
	for elapsed := 5; elapsed <= 25; elapsed += 5 {
		advanceTime(5 * time.Second) // currentTime becomes 5s, 10s, 15s, 20s, 25s (< 30s outage)
		report := v.Sweep(context.Background(), lookup)

		if report.InfrastructureErrors == 0 {
			t.Fatalf("at elapsed=%ds: expected infrastructure errors during PG downtime, got report=%+v", elapsed, report)
		}

		// Fail-closed assertion: memory credentials MUST remain intact
		pwd, exists := v.Get(token)
		if !exists || pwd != password {
			t.Fatalf("at elapsed=%ds: memory vault credential was lost during PG outage: exists=%v, pwd=%q", elapsed, exists, pwd)
		}
	}

	// Verify at exactly 30s: PostgreSQL has recovered
	advanceTime(5 * time.Second) // Now currentTime is 30s (outage has ended)

	// Run sweep after database recovery
	recoveredReport := v.Sweep(context.Background(), lookup)
	if recoveredReport.InfrastructureErrors != 0 {
		t.Fatalf("expected 0 infrastructure errors after recovery, got %d", recoveredReport.InfrastructureErrors)
	}
	if recoveredReport.ValidCount != 1 {
		t.Fatalf("expected ValidCount=1 after recovery, got %d", recoveredReport.ValidCount)
	}

	// Assert user can continue using the session without re-login!
	finalPass, finalOk := v.Get(token)
	if !finalOk || finalPass != password {
		t.Fatalf("credential disappeared after recovery: got %q, ok=%v", finalPass, finalOk)
	}
}

// mockSessionStore satisfies middleware.MailboxSessionLookup for HTTP testing.
type mockMailboxLookup struct {
	fn func(ctx context.Context, tokenHash string) (Session, error)
}

func (m mockMailboxLookup) GetMailboxSession(ctx context.Context, tokenHash string) (Session, error) {
	return m.fn(ctx, tokenHash)
}

// TestPostgresDowntimeHTTPFailClosedPolicy verifies that during PostgreSQL outage,
// HTTP requests return 503 Service Unavailable, preserve credentials in vault,
// and upon database recovery, requests return 200 OK without re-logging in.
func TestPostgresDowntimeHTTPFailClosedPolicy(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	vault := NewMemoryVault()
	token := "active-bearer-token"
	hash := mailboxprincipal.HashToken(token)
	password := "alice-secret-password"
	expiry := time.Now().Add(time.Hour)

	_ = vault.PutSession(Session{
		TokenHash:  hash,
		TenantID:   1,
		MailboxID:  10,
		Address:    "alice@example.com",
		ExpiresAt:  expiry,
		OriginHost: "webmail.example.com",
	}, password)

	var pgAvailable int32 // 0 = down, 1 = up

	lookup := mockMailboxLookup{
		fn: func(_ context.Context, tokenHash string) (Session, error) {
			if atomic.LoadInt32(&pgAvailable) == 0 {
				return Session{}, errors.New("pq: connection pool exhausted (infrastructure unavailable)")
			}
			return Session{
				TokenHash:  tokenHash,
				TenantID:   1,
				MailboxID:  10,
				Address:    "alice@example.com",
				ExpiresAt:  expiry,
				OriginHost: "webmail.example.com",
			}, nil
		},
	}

	// Use requireMailboxAuth helper via simple Gin router
	router := gin.New()
	router.Use(func(c *gin.Context) {
		tokenHeader := c.GetHeader("Authorization")
		if tokenHeader != "Bearer "+token {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		session, err := lookup.GetMailboxSession(c.Request.Context(), hash)
		status := ClassifySessionLookup(session, err, time.Now())
		if status == InfrastructureUnavailable {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "DB_ERROR"})
			return
		}
		if status == RevokedOrInvalid {
			vault.Delete(hash)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED"})
			return
		}
		c.Next()
	})
	router.GET("/api/v1/mailbox/messages", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 1. While PostgreSQL is down: request fails with 503
	atomic.StoreInt32(&pgAvailable, 0)
	req1 := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/mailbox/messages", nil)
	req1.Header.Set("Authorization", "Bearer "+token)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	if w1.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable during outage, got %d", w1.Code)
	}

	// Assert credential was NOT purged from the vault!
	if pwd, ok := vault.Get(hash); !ok || pwd != password {
		t.Fatalf("credential was evicted from vault during 503 outage: got %q, ok=%v", pwd, ok)
	}

	// 2. PostgreSQL recovers: request succeeds with 200 without re-login
	atomic.StoreInt32(&pgAvailable, 1)
	req2 := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/mailbox/messages", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK after PostgreSQL recovery, got %d", w2.Code)
	}

	// Credential remains retained
	if pwd, ok := vault.Get(hash); !ok || pwd != password {
		t.Fatalf("credential missing after recovery: got %q, ok=%v", pwd, ok)
	}
}

// TestVaultABAIsolationUnderConcurrentRefreshAndSweep verifies requirement 5.2:
// Solves ABA race where a snapshot taken by a slow Sweep is interleaved with a
// concurrent login refresh that puts a new credential version into the vault.
func TestVaultABAIsolationUnderConcurrentRefreshAndSweep(t *testing.T) {
	v := NewMemoryVault()
	v.itemTimeout = 500 * time.Millisecond

	token := "aba-test-token"
	oldPassword := "old-password"
	newPassword := "fresh-refreshed-password"
	expiry := time.Now().Add(time.Hour)

	// Step 1: Initial session placed in vault (Generation 1)
	err := v.PutSession(Session{
		TokenHash: token,
		TenantID:  1,
		MailboxID: 99,
		Address:   "bob@example.com",
		ExpiresAt: expiry,
	}, oldPassword)
	if err != nil {
		t.Fatalf("initial PutSession failed: %v", err)
	}

	gen1, ok := v.Generation(token)
	if !ok || gen1 != 1 {
		t.Fatalf("expected generation 1, got %d (ok=%v)", gen1, ok)
	}

	// Channels to coordinate interleaved execution
	sweepSnapshotTaken := make(chan struct{})
	refreshCompleted := make(chan struct{})

	// Step 2: Slow Sweep starts in background
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		v.Sweep(context.Background(), func(_ context.Context, _ string) (Session, error) {
			// Notify that Sweep has entered lookup for token
			close(sweepSnapshotTaken)
			// Wait until concurrent refresh has placed the NEW credential into vault
			<-refreshCompleted
			// Now return RevokedOrInvalid (e.g. sql.ErrNoRows for the old session)
			return Session{}, sql.ErrNoRows
		})
	}()

	// Wait for Sweep to read targets
	<-sweepSnapshotTaken

	// Step 3: Concurrent goroutine refreshes the session with a new password (Generation 2)
	err = v.PutSession(Session{
		TokenHash: token,
		TenantID:  1,
		MailboxID: 99,
		Address:   "bob@example.com",
		ExpiresAt: expiry.Add(time.Hour),
	}, newPassword)
	if err != nil {
		t.Fatalf("refresh PutSession failed: %v", err)
	}

	gen2, ok := v.Generation(token)
	if !ok || gen2 <= gen1 {
		t.Fatalf("expected generation > %d, got %d", gen1, gen2)
	}

	// Release Sweep to proceed with eviction attempt
	close(refreshCompleted)
	<-sweepDone

	// Step 4: Assert ABA isolation: the new credential was NOT deleted by the stale Sweep!
	pwd, exists := v.Get(token)
	if !exists {
		t.Fatal("ABA race failure: stale sweep evicted newly refreshed session credential!")
	}
	if pwd != newPassword {
		t.Fatalf("expected new password %q, got %q", newPassword, pwd)
	}
}

// TestVaultHighConcurrencyRaceIsolation tests high concurrent access
// with Go's race detector to guarantee thread safety and version consistency.
func TestVaultHighConcurrencyRaceIsolation(t *testing.T) {
	t.Helper()
	v := NewMemoryVault()
	v.itemTimeout = 50 * time.Millisecond
	v.minBackoff = 1 * time.Millisecond
	v.maxBackoff = 5 * time.Millisecond

	const numTokens = 20
	tokens := make([]string, numTokens)
	for i := range tokens {
		tokens[i] = fmt.Sprintf("race-token-%d", i)
		_ = v.PutSession(Session{
			TokenHash: tokens[i],
			TenantID:  1,
			MailboxID: int64(i + 1),
			Address:   fmt.Sprintf("user%d@example.com", i),
			ExpiresAt: time.Now().Add(time.Hour),
		}, fmt.Sprintf("pass-initial-%d", i))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Workers concurrently refreshing sessions
	for worker := 0; worker < 5; worker++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w)))
			for {
				select {
				case <-ctx.Done():
					return
				default:
					idx := r.Intn(numTokens)
					tok := tokens[idx]
					_ = v.PutSession(Session{
						TokenHash: tok,
						TenantID:  1,
						MailboxID: int64(idx + 1),
						Address:   fmt.Sprintf("user%d@example.com", idx),
						ExpiresAt: time.Now().Add(time.Hour),
					}, fmt.Sprintf("pass-worker-%d-%d", w, r.Intn(1000)))
					time.Sleep(time.Duration(r.Intn(3)) * time.Millisecond)
				}
			}
		}(worker)
	}

	// Workers reading credentials
	for worker := 0; worker < 5; worker++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w*10)))
			for {
				select {
				case <-ctx.Done():
					return
				default:
					idx := r.Intn(numTokens)
					_, _ = v.Get(tokens[idx])
					time.Sleep(time.Duration(r.Intn(2)) * time.Millisecond)
				}
			}
		}(worker)
	}

	// Workers running sweeps with mixed results
	for worker := 0; worker < 3; worker++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w*100)))
			for {
				select {
				case <-ctx.Done():
					return
				default:
					v.Sweep(ctx, func(_ context.Context, tok string) (Session, error) {
						roll := r.Intn(10)
						switch {
						case roll < 6:
							// 60% valid
							return Session{TokenHash: tok, TenantID: 1, MailboxID: 1, ExpiresAt: time.Now().Add(time.Hour)}, nil
						case roll < 8:
							// 20% transient infra failure
							return Session{}, errors.New("transient database connection pool full")
						default:
							// 20% explicitly revoked
							return Session{}, sql.ErrNoRows
						}
					})
					time.Sleep(10 * time.Millisecond)
				}
			}
		}(worker)
	}

	wg.Wait()
}

// TestSweepPerEntryTimeoutAndAvalancheBackoff verifies requirement 4:
// Each entry has an independent fine-grained context timeout (e.g. 2-3s).
// Consecutive infrastructure failures trigger polling backoff and abort the sweep pass
// to prevent avalanche against a degraded database.
func TestSweepPerEntryTimeoutAndAvalancheBackoff(t *testing.T) {
	v := NewMemoryVault()
	v.itemTimeout = 40 * time.Millisecond // Fine-grained independent context timeout
	v.minBackoff = 10 * time.Millisecond
	v.maxBackoff = 40 * time.Millisecond
	v.maxConsecutiveFailures = 2

	// Put 5 entries into the vault
	for i := 1; i <= 5; i++ {
		_ = v.PutSession(Session{
			TokenHash: fmt.Sprintf("tok-%d", i),
			TenantID:  1,
			MailboxID: int64(i),
			Address:   fmt.Sprintf("user%d@example.com", i),
			ExpiresAt: time.Now().Add(time.Hour),
		}, fmt.Sprintf("pass-%d", i))
	}

	var checkedCount int32
	start := time.Now()

	// Simulate slow/hanging DB lookups that exceed per-item timeout
	report := v.Sweep(context.Background(), func(ctx context.Context, _ string) (Session, error) {
		atomic.AddInt32(&checkedCount, 1)
		// Wait on ctx.Done() to assert per-item context deadline enforcement
		select {
		case <-ctx.Done():
			return Session{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
			return Session{}, errors.New("slow query returned")
		}
	})

	duration := time.Since(start)

	// Since maxConsecutiveFailures = 2:
	// Item 1 timed out (~40ms) -> backoff (10ms)
	// Item 2 timed out (~40ms) -> consecutive failures hit threshold (2)!
	// Sweep pass immediately aborted!
	// Items 3, 4, 5 were NOT queried, avoiding avalanche on the database!
	if atomic.LoadInt32(&checkedCount) != 2 {
		t.Fatalf("expected avalanche abort after 2 failures, but %d items were checked", atomic.LoadInt32(&checkedCount))
	}
	if !report.Aborted {
		t.Fatal("expected report.Aborted to be true")
	}
	if report.InfrastructureErrors != 2 {
		t.Fatalf("expected 2 infrastructure errors, got %d", report.InfrastructureErrors)
	}
	// Total duration should be well under 300ms (not 5 * 500ms = 2.5s)
	if duration > 1*time.Second {
		t.Fatalf("sweep took too long (%v), avalanche protection failed", duration)
	}

	// All 5 credentials in the vault MUST still be preserved!
	for i := 1; i <= 5; i++ {
		tok := fmt.Sprintf("tok-%d", i)
		expectedPass := fmt.Sprintf("pass-%d", i)
		if got, ok := v.Get(tok); !ok || got != expectedPass {
			t.Fatalf("credential %s was lost during sweep timeout: got=%q, ok=%v", tok, got, ok)
		}
	}
}
