package dovecotauth

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// openMainStore triggers the process-global sqlite connection hook exactly
// like the running daemon does: every test below runs with the hook active,
// so the auth snapshot must survive it without flipping to WAL.
func openMainStore(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlitedb.Open(context.Background(), filepath.Join(t.TempDir(), "main.db"))
	if err != nil {
		t.Fatalf("open main store (registers the pragma hook): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func journalMode(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var mode string
	if err := db.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	return mode
}

func sidecars(t *testing.T, path string) []string {
	t.Helper()
	var found []string
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			found = append(found, suffix)
		}
	}
	return found
}

// TestWALFixReconcileKeepsDeleteJournal covers acceptance (a): after the
// global hook is active, src == snap Reconcile returns nil and the snapshot
// stays DELETE-journal with zero sidecars, readable under 0555/0444
// permissions.
func TestWALFixReconcileKeepsDeleteJournal(t *testing.T) {
	mainDB := openMainStore(t)
	src := fixtureSource()
	manager, path := newTestManager(t, src)
	if _, err := manager.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := journalMode(t, path); got != "delete" {
		t.Fatalf("fresh snapshot journal mode %q", got)
	}

	// src == snap: reconcile must be a no-op that leaves the journal alone.
	if err := manager.Reconcile(context.Background(), 0); err != nil {
		t.Fatalf("src == snap reconcile drifted: %v", err)
	}
	if got := journalMode(t, path); got != "delete" {
		t.Fatalf("post-reconcile journal mode %q, want delete", got)
	}
	if sidecars := sidecars(t, path); len(sidecars) != 0 {
		t.Fatalf("post-reconcile sidecars %v", sidecars)
	}

	// Read-only permissions: the read-only consumer (dovecot uid) opens the
	// file without any write capability.
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err = db.QueryRowContext(context.Background(), `SELECT count(*) FROM mailboxes`).Scan(&n); err != nil {
		t.Fatalf("read-only query under 0444: %v", err)
	}
	_ = mainDB
}

// TestWALFixReconcileRepairsDriftedSnapshot covers acceptance (b): a
// snapshot flipped to WAL (with sidecars) is reported as drift by Reconcile
// and restored to DELETE + zero sidecars by Repair.
func TestWALFixReconcileRepairsDriftedSnapshot(t *testing.T) {
	openMainStore(t)
	src := fixtureSource()
	manager, path := newTestManager(t, src)
	if _, err := manager.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Simulate exactly what the global hook used to do: flip the published
	// snapshot to WAL.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(context.Background(), `PRAGMA journal_mode=wal`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if got := journalMode(t, path); got != "wal" {
		t.Fatalf("setup: journal mode %q, want wal", got)
	}

	err = manager.Reconcile(context.Background(), 0)
	if err == nil {
		t.Fatal("drifted snapshot must be reported by Reconcile")
	}
	if !strings.Contains(err.Error(), "journal drift") && !strings.Contains(err.Error(), "wal") {
		t.Fatalf("drift error %v does not name the journal drift", err)
	}
	if _, err = manager.Repair(context.Background()); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if got := journalMode(t, path); got != "delete" {
		t.Fatalf("post-repair journal mode %q, want delete", got)
	}
	if sidecars := sidecars(t, path); len(sidecars) != 0 {
		t.Fatalf("post-repair sidecars %v", sidecars)
	}
}

// TestWALFixApplyKeepsDeleteJournal covers acceptance (c): after Apply
// compensations the republished snapshot stays DELETE-journal with zero
// sidecars and remains readable at read-only permissions.
func TestWALFixApplyKeepsDeleteJournal(t *testing.T) {
	openMainStore(t)
	src := fixtureSource()
	manager, path := newTestManager(t, src)
	if _, err := manager.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := manager.Apply(context.Background(),
		Change{Address: "alice@example.test", DomainID: 10, TenantID: 1}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := journalMode(t, path); got != "delete" {
		t.Fatalf("post-apply journal mode %q, want delete", got)
	}
	if sidecars := sidecars(t, path); len(sidecars) != 0 {
		t.Fatalf("post-apply sidecars %v", sidecars)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var password string
	if err = db.QueryRowContext(context.Background(), `SELECT password_hash FROM mailboxes WHERE address = 'alice@example.test'`).Scan(&password); err != nil {
		t.Fatalf("read-only query after apply: %v", err)
	}
}

// TestWALFixAssertPragmasNoRegression covers acceptance (d): the sqlitedb
// multi-connection pool contract (foreign_keys/busy_timeout/WAL/synchronous
// + AssertPragmas) is unchanged with the narrowed hook.
func TestWALFixAssertPragmasNoRegression(t *testing.T) {
	mainDB := openMainStore(t)
	var fk, busy, journal, synchronous string
	if err := mainDB.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if err := mainDB.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if err := mainDB.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := mainDB.QueryRowContext(context.Background(), `PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if fk != "1" {
		t.Fatalf("foreign_keys=%s", fk)
	}
	if busy == "0" {
		t.Fatal("busy_timeout not set")
	}
	if journal != "wal" {
		t.Fatalf("main store journal_mode=%s, want wal", journal)
	}
	if synchronous == "0" {
		t.Fatalf("synchronous=%s", synchronous)
	}
	if err := sqlitedb.AssertPragmas(context.Background(), mainDB); err != nil {
		t.Fatalf("AssertPragmas regression: %v", err)
	}
}
