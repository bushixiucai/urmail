package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// newClaimTestDB opens a real SQLite database (WAL + BEGIN IMMEDIATE) with the
// ported schema and the minimal fixture a claim statement needs. The store's
// DBTX is still the pgx surface (db.go is intentionally held until the Phase 2
// wiring), so this test drives the generated/handwritten SQL text directly:
// the claim predicates are what B5b has to keep honest, and they only mean
// something on the engine that will execute them.
// readMigration reads one migration from the checked-in SQLite schema.
func readMigration(name string) (string, error) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations_sqlite", name))
	return string(raw), err
}

func newClaimTestDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "claims.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, name := range []string{"0001_init.sql", "0002_write_side_effects.sql"} {
		body, readErr := readMigration(name)
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if _, execErr := db.ExecContext(ctx, body); execErr != nil {
			t.Fatalf("apply %s: %v", name, execErr)
		}
	}
	for _, stmt := range []string{
		`INSERT INTO brands(id,slug,name) VALUES(1,'claims','Claims')`,
		`INSERT INTO users(id,email,password_hash,status) VALUES(3,'claims@example.test','x','active')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status,owner_user_id) VALUES(7,1,'Claims','claims','active',3)`,
		`INSERT INTO domains(id,tenant_id,fqdn,status,verification_token,verified_at,provisioning_state)
		 VALUES(31,7,'example.test','active','tok',1,'pending')`,
		// domain_provisioning_insert_pending forces a pending state; only an
		// explicit UPDATE models an already-provisioned domain.
		`UPDATE domains SET provisioning_state='ready' WHERE id=31`,
		`INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,send_enabled)
		 VALUES(11,7,31,'claims','claims@example.test','x',-1,'active',1)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return db, ctx
}

// TestQuotaClaimAllowsExactlyOneWinner races two senders against a daily limit
// of one. The ON CONFLICT ... WHERE messages_sent < limit predicate is the
// whole gate: if it leaks, both goroutines send.
func TestQuotaClaimAllowsExactlyOneWinner(t *testing.T) {
	db, ctx := newClaimTestDB(t)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',7,'mailbox_daily_send_limit','1')`); err != nil {
		t.Fatalf("seed limit: %v", err)
	}

	var wg sync.WaitGroup
	results := make([]bool, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = takeDailyQuota(ctx, db, 11, 7, 1)
		}(i)
	}
	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("claim errors: %v / %v", errs[0], errs[1])
	}
	if results[0] == results[1] {
		t.Fatalf("want exactly one winner, got %v/%v", results[0], results[1])
	}
	var sent int64
	if err := db.QueryRowContext(ctx,
		`SELECT messages_sent FROM mailbox_usage WHERE mailbox_id=11`).Scan(&sent); err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if sent != 1 {
		t.Fatalf("messages_sent = %d, want 1 (the losing claim must not increment)", sent)
	}
}

// TestQuotaClaimBothSucceedUnderHigherLimit is the control: the same race with
// room for two must not serialize into one winner.
func TestQuotaClaimBothSucceedUnderHigherLimit(t *testing.T) {
	db, ctx := newClaimTestDB(t)

	var wg sync.WaitGroup
	results := make([]bool, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = takeDailyQuota(ctx, db, 11, 7, 2)
		}(i)
	}
	wg.Wait()

	if errs[0] != nil || errs[1] != nil || !results[0] || !results[1] {
		t.Fatalf("both claims should succeed: %v/%v errors %v/%v", results[0], results[1], errs[0], errs[1])
	}
	var sent int64
	if err := db.QueryRowContext(ctx,
		`SELECT messages_sent FROM mailbox_usage WHERE mailbox_id=11`).Scan(&sent); err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if sent != 2 {
		t.Fatalf("messages_sent = %d, want 2", sent)
	}
}

// TestEASClaimIsSingleWinner relies on the unique idempotency key plus
// ON CONFLICT DO NOTHING RETURNING: two agents racing on the same send may
// create the job only once.
func TestEASClaimIsSingleWinner(t *testing.T) {
	db, ctx := newClaimTestDB(t)

	var wg sync.WaitGroup
	winners := make([]bool, 2)
	errs := make([]error, 2)
	for i := range winners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var id int64
			// BEGIN IMMEDIATE is the connection's DSN default; the second
			// writer waits on the busy timeout instead of failing.
			err := db.QueryRowContext(ctx, claimEASSend, 7, 11, "same-key", []byte(`{"x":1}`)).Scan(&id)
			switch {
			case err == nil:
				winners[i] = id > 0
			case errors.Is(err, sql.ErrNoRows):
				winners[i] = false
			default:
				errs[i] = err
			}
		}(i)
	}
	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("claim errors: %v / %v", errs[0], errs[1])
	}
	if winners[0] == winners[1] {
		t.Fatalf("want exactly one EAS job, got %v/%v", winners[0], winners[1])
	}
	var jobs int64
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM mailbox_jobs WHERE tenant_id=7 AND idempotency_key='same-key'`).Scan(&jobs); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobs != 1 {
		t.Fatalf("mailbox_jobs rows = %d, want 1", jobs)
	}
}

// takeDailyQuota executes the same statement as PlatformTakeMailboxDailyQuota
// with the limit supplied directly, so the race is on the SQL predicate rather
// than on the settings read that precedes it.
func takeDailyQuota(ctx context.Context, db *sql.DB, mailboxID, tenantID, limit int64) (bool, error) {
	var sent int64
	err := db.QueryRowContext(ctx, takeMailboxQuotaSQL, mailboxID, tenantID, "2026-11-20", limit).Scan(&sent)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
