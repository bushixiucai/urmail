package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
)

// TestBackupAutoPlatformPoolSQLiteIgnoresPostgresDSN reproduces the run5
// failure: in single-engine mode the platform pool must come from the SQLite
// file, never from the (nonexistent) PostgreSQL DSN, and the
// settings probe must still work.
func TestBackupAutoPlatformPoolSQLiteIgnoresPostgresDSN(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlitePath := buildRestoreFixture(t, dir, "urmail.sqlite", 1)
	cfg := &config.Config{
		DBEngine:            "sqlite",
		SQLitePath:          sqlitePath,
		DatabaseURLPlatform: "postgres://root@/urmail?host=/tmp/.s.PGSQL.5432&sslmode=disable",
	}
	pool, err := openBackupPlatformPool(ctx, cfg)
	if err != nil {
		t.Fatalf("sqlite platform pool: %v", err)
	}
	defer pool.Close()

	// The offsite settings probe runs on the SQLite pool; an unset row means
	// "no destination configured", not a connection failure.
	if _, _, _, disabled, err := offsiteDestinationFromSettings(ctx, cfg, ""); err != nil || !disabled {
		t.Fatalf("settings probe disabled=%v err=%v", disabled, err)
	}

	if filepath.Base(sqlitePath) != "urmail.sqlite" {
		t.Fatalf("fixture path drifted: %s", sqlitePath)
	}
}
