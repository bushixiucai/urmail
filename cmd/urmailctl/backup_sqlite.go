package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// Seams for the failure-path regression tests.
var sqliteSnapshotChmod = os.Chmod
var sqliteSnapshotVacuum = func(ctx context.Context, src *sql.DB, target string) error {
	_, err := src.ExecContext(ctx, `VACUUM INTO ?`, target)
	return err
}

// createSQLiteSnapshot writes a standalone, consistent SQLite snapshot with
// VACUUM INTO. It runs under the same fence as the PostgreSQL dump path:
// writers are stopped by --maintenance, and WAL content is checkpointed into
// the main file before VACUUM INTO reads it. The snapshot is opened once more
// to assert integrity_check=ok and the expected user_version before the caller
// records it as a backup artifact.
func createSQLiteSnapshot(ctx context.Context, path, target string) error {
	// Both paths are operator-configured absolute paths; a traversal-bearing or
	// relative value is refused before any directory is created or read.
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return fmt.Errorf("SQLite snapshot target must be a clean absolute path")
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("SQLite backup requires URMAIL_SQLITE_PATH")
	}
	src, err := sqlitedb.OpenExisting(ctx, path, sqlitedb.ExpectedSchemaVersion)
	if err != nil {
		return fmt.Errorf("open sqlite for backup: %w", err)
	}
	defer func() { _ = src.Close() }()
	if _, err = src.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("sqlite backup checkpoint: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if _, statErr := os.Stat(target); statErr == nil {
		return fmt.Errorf("SQLite snapshot target already exists: %s", target)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	// Never leave a half-finished snapshot behind: the target did not exist
	// before this call, so every failure from here on (VACUUM itself,
	// permissions, reopen, integrity) removes it again.
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(target)
		}
	}()
	if err = sqliteSnapshotVacuum(ctx, src, target); err != nil {
		return fmt.Errorf("sqlite snapshot: %w", err)
	}
	if err = sqliteSnapshotChmod(target, 0o600); err != nil {
		return err
	}
	snap, err := sqlitedb.OpenExisting(ctx, target, sqlitedb.ExpectedSchemaVersion)
	if err != nil {
		return fmt.Errorf("verify sqlite snapshot: %w", err)
	}
	defer func() { _ = snap.Close() }()
	var integrity string
	if err = snap.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("sqlite snapshot integrity: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("sqlite snapshot integrity_check=%s", integrity)
	}
	complete = true
	return nil
}
