package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// sqliteSnapshotChmod is a seam for the failure-path regression test.
var sqliteSnapshotChmod = os.Chmod

// createSQLiteSnapshot writes a standalone, consistent SQLite snapshot with
// VACUUM INTO. It runs under the same fence as the PostgreSQL dump path:
// writers are stopped by --maintenance, and WAL content is checkpointed into
// the main file before VACUUM INTO reads it. The snapshot is opened once more
// to assert integrity_check=ok and the expected user_version before the caller
// records it as a backup artifact.
func createSQLiteSnapshot(ctx context.Context, path, target string) error {
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
	if _, err = src.ExecContext(ctx, `VACUUM INTO ?`, target); err != nil {
		return fmt.Errorf("sqlite snapshot: %w", err)
	}
	// Never leave a half-finished snapshot behind: any failure after VACUUM
	// INTO (permissions, reopen, integrity) removes the target again.
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(target)
		}
	}()
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
