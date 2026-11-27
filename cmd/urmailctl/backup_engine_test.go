package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
)

// TestBackupDatabaseArtifactEngineExclusive pins the dual-engine contract:
// with IsSQLite() the run produces a VACUUM INTO snapshot and can never reach
// pg_dump (the DSN below is deliberately unusable), while PostgreSQL mode
// takes the dump path and produces no snapshot.
func TestBackupDatabaseArtifactEngineExclusive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := buildRestoreFixture(t, dir, "live.sqlite", 1)
	const stamp = "20260101T000000Z"

	sqliteCfg := &config.Config{DBEngine: "sqlite", SQLitePath: live, DatabaseURLMigrate: "postgres://urmail_migrate:secret@127.0.0.1:1/urmail?sslmode=disable"}
	out := t.TempDir()
	artifact, err := backupDatabaseArtifact(ctx, sqliteCfg, out, stamp)
	if err != nil {
		t.Fatalf("sqlite mode: %v", err)
	}
	if filepath.Ext(artifact) != ".sqlite" {
		t.Fatalf("sqlite mode produced %s", artifact)
	}
	if _, err = os.Stat(artifact); err != nil {
		t.Fatalf("sqlite artifact missing: %v", err)
	}
	if _, err = os.Stat(filepath.Join(out, "urmail-"+stamp+".dump")); err == nil {
		t.Fatal("sqlite mode produced a pg_dump artifact")
	}

	pgCfg := &config.Config{DatabaseURLMigrate: "postgres://urmail_migrate:secret@127.0.0.1:1/urmail?sslmode=disable"}
	pgOut := t.TempDir()
	if _, err = backupDatabaseArtifact(ctx, pgCfg, pgOut, stamp); err == nil {
		t.Fatal("postgres mode with an unreachable DSN succeeded")
	}
	if _, err = os.Stat(filepath.Join(pgOut, "urmail-"+stamp+".sqlite")); err == nil {
		t.Fatal("postgres mode produced a SQLite snapshot")
	}
}

// TestSQLiteRestoreRequiresLivePath keeps the single-engine restore gate: the
// S6 fail-closed refusal is gone, but a missing live database path still
// refuses before any file is replaced.
func TestSQLiteRestoreRequiresLivePath(t *testing.T) {
	err := restoreSQLiteDatabase(context.Background(), &config.Config{DBEngine: "sqlite"}, "/tmp/snap.sqlite")
	if err == nil || !strings.Contains(err.Error(), "URMAIL_SQLITE_PATH") {
		t.Fatalf("sqlite restore gate err=%v", err)
	}
}
