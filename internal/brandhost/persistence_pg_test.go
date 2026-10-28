package brandhost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func ts(t testing.TB, v *time.Time) pgtype.Timestamptz {
	t.Helper()
	if v == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *v, Valid: true}
}

func textPtr(v string) pgtype.Text { return pgtype.Text{String: v, Valid: v != ""} }

// TestBrandHostLifecyclePersistence exercises the real schema end to end:
// binding resets to pending-verify, the challenge is single use at the SQL
// level, and lifecycle updates fail closed for unknown hosts.
// Skipped automatically when URMAIL_TEST_DATABASE_URL is not set.
func TestBrandHostLifecyclePersistence(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	queries := store.New(platform)

	var brand int64
	if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('ops101','OPS101') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}

	if _, err := queries.PlatformBindBrandHost(ctx, store.PlatformBindBrandHostParams{Hostname: "brand.example.test", ID: brand}); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := queries.PlatformGetBrandHostLifecycle(ctx, "brand.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.Status != string(StatusPendingVerify) {
		t.Fatalf("initial status = %q", lifecycle.Status)
	}
	if Status(lifecycle.Status).CertEligible() {
		t.Fatalf("pending-verify host must not be cert eligible")
	}
	eligible, err := queries.PlatformBrandHostCertEligible(ctx, "brand.example.test")
	if err != nil || eligible {
		t.Fatalf("pending-verify eligible=%v err=%v", eligible, err)
	}

	// Challenge round trip: insert, consume exactly once.
	ch, err := NewChallenge("brand.example.test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = queries.PlatformInsertBrandHostChallenge(ctx, store.PlatformInsertBrandHostChallengeParams{Hostname: ch.Hostname, TokenDigest: ch.Digest, Method: ch.Method, ExpiresAt: ts(t, &ch.Expires)}); err != nil {
		t.Fatal(err)
	}
	record, err := queries.PlatformGetBrandHostChallenge(ctx, ch.Hostname)
	if err != nil {
		t.Fatal(err)
	}
	var consumedAt *time.Time
	if record.ConsumedAt.Valid {
		tm := record.ConsumedAt.Time
		consumedAt = &tm
	}
	persisted := ChallengeRecord{TokenDigest: record.TokenDigest, ExpiresAt: record.ExpiresAt.Time, ConsumedAt: consumedAt}
	if err := CheckChallenge(persisted, ch.Token, time.Now()); err != nil {
		t.Fatalf("fresh challenge rejected: %v", err)
	}
	consumed, err := queries.PlatformConsumeBrandHostChallenge(ctx, ch.Hostname)
	if err != nil || consumed != 1 {
		t.Fatalf("first consume: rows=%d err=%v", consumed, err)
	}
	// Hostile replay: the SQL guard refuses a second consumption.
	replayed, err := queries.PlatformConsumeBrandHostChallenge(ctx, ch.Hostname)
	if err != nil || replayed != 0 {
		t.Fatalf("replayed consume: rows=%d err=%v", replayed, err)
	}

	// Walk the state machine through persistence.
	now := time.Now()
	step := func(event Event, reason string) HostState {
		t.Helper()
		current, err := queries.PlatformGetBrandHostLifecycle(ctx, lifecycle.Hostname)
		if err != nil {
			t.Fatal(err)
		}
		next, err := Transition(HostState{Hostname: current.Hostname, Status: Status(current.Status)}, event, now, reason)
		if err != nil {
			t.Fatalf("transition %s: %v", event, err)
		}
		n, err := queries.PlatformSetBrandHostLifecycle(ctx, store.PlatformSetBrandHostLifecycleParams{
			Hostname: next.Hostname, Status: string(next.Status), FailureReason: textPtr(reason),
			VerifiedAt: ts(t, next.VerifiedAt), CertIssuedAt: ts(t, next.CertIssuedAt), ActivatedAt: ts(t, next.ActivatedAt),
		})
		if err != nil || n != 1 {
			t.Fatalf("persist %s: rows=%d err=%v", event, n, err)
		}
		row, err := queries.PlatformGetBrandHostLifecycle(ctx, next.Hostname)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != string(next.Status) {
			t.Fatalf("persisted status %q want %q", row.Status, next.Status)
		}
		return next
	}
	step(EventOwnershipVerified, "")
	eligible, err = queries.PlatformBrandHostCertEligible(ctx, "brand.example.test")
	if err != nil || !eligible {
		t.Fatalf("verified host not eligible: %v %v", eligible, err)
	}
	step(EventCertIssued, "")
	// A live binding cannot be overwritten to restart proof. Explicit revoke
	// invalidates the old challenge before a fresh binding may be created.
	if _, e := queries.PlatformBindBrandHost(ctx, store.PlatformBindBrandHostParams{Hostname: "brand.example.test", ID: brand}); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("live binding overwrite accepted", e)
	}
	if _, e := queries.PlatformUnbindBrandHost(ctx, "brand.example.test"); e != nil {
		t.Fatal(e)
	}
	if _, err := queries.PlatformBindBrandHost(ctx, store.PlatformBindBrandHostParams{Hostname: "brand.example.test", ID: brand}); err != nil {
		t.Fatal(err)
	}
	rebound, err := queries.PlatformGetBrandHostLifecycle(ctx, "brand.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if rebound.Status != string(StatusPendingVerify) || rebound.VerifiedAt.Valid {
		t.Fatalf("rebinding did not reset lifecycle: %+v", rebound)
	}
	eligible, _ = queries.PlatformBrandHostCertEligible(ctx, "brand.example.test")
	if eligible {
		t.Fatalf("reset host still cert eligible")
	}

	// Unknown host never becomes eligible (fail closed).
	unknown, err := queries.PlatformBrandHostCertEligible(ctx, "nope.example.test")
	if err != nil || unknown {
		t.Fatalf("unknown host eligible=%v err=%v", unknown, err)
	}
}
