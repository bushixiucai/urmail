package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

func setSqlitePreflightEnv(t *testing.T, path string) {
	t.Helper()
	t.Setenv("URMAIL_DB_ENGINE", "sqlite")
	t.Setenv("URMAIL_SQLITE_PATH", path)
	// Single-engine configuration carries no PostgreSQL DSN; that is exactly
	// why the old gate never read the SQLite ledger.
	t.Setenv("URMAIL_DATABASE_URL", "")
	t.Setenv("URMAIL_DATABASE_URL_PLATFORM", "")
	t.Setenv("URMAIL_LISTEN_ADDR", "127.0.0.1:18120")
	t.Setenv("URMAIL_POLICY_ENABLED", "true")
	t.Setenv("URMAIL_MYHOSTNAME", "box.example.test")
}

func sqliteLedger(t *testing.T, path string) *backupauto.StateRecorder {
	t.Helper()
	raw, err := sqlitedb.OpenExisting(context.Background(), path, sqlitedb.ExpectedSchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.NewSQLitePool(raw, isolation.ScopePlatform)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &backupauto.StateRecorder{Pool: pool}
}

func seedSQLiteRun(t *testing.T, recorder *backupauto.StateRecorder, success bool, offsite string) {
	t.Helper()
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102T150405Z")
	uid, err := recorder.StartRun(ctx, backupauto.ScopeComplete, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if offsite != "" {
		if err = recorder.SetOffsiteState(ctx, uid, offsite); err != nil {
			t.Fatal(err)
		}
	}
	if err = recorder.FinishRun(ctx, uid, success, nil, 1024, time.Second, "urmail-"+stamp+".manifest.json", "", 0); err != nil {
		t.Fatal(err)
	}
}

// TestPreflightFreshBackupSQLiteReadsLedger is the F1 third site: with a dead
// PostgreSQL DSN the gate must still read the SQLite backup_runs rows and
// decide from them, never degrade to a pass.
func TestPreflightFreshBackupSQLiteReadsLedger(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := buildRestoreFixture(t, dir, "urmail.sqlite", 1)
	setSqlitePreflightEnv(t, path)

	seedSQLiteRun(t, sqliteLedger(t, path), true, backupauto.OffsiteStatePendingLocalOnly)
	msg, err := preflightFreshBackup(ctx, time.Hour)
	if err != nil || msg != "" {
		t.Fatalf("successful pending_local_only row: msg=%q err=%v", msg, err)
	}

	failedPath := buildRestoreFixture(t, t.TempDir(), "urmail.sqlite", 1)
	setSqlitePreflightEnv(t, failedPath)
	seedSQLiteRun(t, sqliteLedger(t, failedPath), false, "")
	if _, err = preflightFreshBackup(ctx, time.Hour); err == nil {
		t.Fatal("failed newest run was accepted")
	}
}

func TestPreflightFreshBackupSQLiteMissingTableRefuses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "empty.sqlite")
	raw, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	setSqlitePreflightEnv(t, path)
	if msg, err := preflightFreshBackup(ctx, time.Hour); err == nil {
		t.Fatalf("missing backup_runs accepted: msg=%q", msg)
	}
}

// TestPreflightFreshBackupPostgresKeepsDegraded pins the PostgreSQL escape:
// an unreachable platform DB still degrades to a recorded warning, not a hard
// failure, exactly as before.
func TestPreflightFreshBackupPostgresKeepsDegraded(t *testing.T) {
	t.Setenv("URMAIL_DB_ENGINE", "")
	t.Setenv("URMAIL_SQLITE_PATH", "")
	t.Setenv("URMAIL_DATABASE_URL", "postgres://urmail_migrate:secret@127.0.0.1:1/urmail?sslmode=disable")
	t.Setenv("URMAIL_DATABASE_URL_PLATFORM", "postgres://urmail_migrate:secret@127.0.0.1:1/urmail?sslmode=disable")
	t.Setenv("URMAIL_LISTEN_ADDR", "127.0.0.1:18121")
	msg, err := preflightFreshBackup(context.Background(), time.Hour)
	if err != nil || msg == "" {
		t.Fatalf("postgres degraded path: msg=%q err=%v", msg, err)
	}
}
