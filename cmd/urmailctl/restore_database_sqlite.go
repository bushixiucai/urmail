package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/dovecotauth"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// restoreSQLiteSnapshot swaps a VACUUM INTO snapshot in for the live database.
// The sequence is fixed because a WAL file from the old generation must never
// be replayed onto the new main file:
//
//  1. PRAGMA wal_checkpoint(TRUNCATE) folds the live WAL into the main file;
//  2. any remaining -wal/-shm sidecar is removed (fail-closed if it cannot be);
//  3. the snapshot is copied to a private sibling, verified (integrity_check
//     and user_version), fsynced, and atomically renamed over the main file;
//  4. the restored database is opened once more to assert integrity_check=ok
//     and the expected user_version.
//
// Callers run this inside the restore fence; the dovecot auth snapshot rebuild
// is a separate post-restore step and must complete before release.
func restoreSQLiteSnapshot(ctx context.Context, snapshot, livePath string, expectedVersion int) error {
	if _, err := os.Stat(snapshot); err != nil {
		return fmt.Errorf("sqlite restore snapshot: %w", err)
	}
	live, err := sqlitedb.OpenExisting(ctx, livePath, expectedVersion)
	if err != nil {
		return fmt.Errorf("open live sqlite for restore: %w", err)
	}
	var busy, logFrames, checkpointed int
	if err = live.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		_ = live.Close()
		return fmt.Errorf("sqlite restore checkpoint: %w", err)
	}
	if err = live.Close(); err != nil {
		return err
	}
	if busy != 0 || logFrames != checkpointed {
		return fmt.Errorf("sqlite restore checkpoint incomplete (busy=%d log=%d checkpointed=%d)", busy, logFrames, checkpointed)
	}
	for _, sidecar := range []string{livePath + "-wal", livePath + "-shm"} {
		if err = os.Remove(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove sqlite sidecar %s: %w", filepath.Base(sidecar), err)
		}
	}

	staged := livePath + ".restore-tmp"
	_ = os.Remove(staged)
	if err = copyFilePrivate(snapshot, staged); err != nil {
		return fmt.Errorf("stage sqlite snapshot: %w", err)
	}
	defer func() { _ = os.Remove(staged) }()
	if err = verifySQLiteFile(ctx, staged, expectedVersion); err != nil {
		return fmt.Errorf("verify staged sqlite snapshot: %w", err)
	}
	if err = os.Rename(staged, livePath); err != nil {
		return fmt.Errorf("replace live sqlite database: %w", err)
	}
	if err = os.Chmod(livePath, 0o600); err != nil {
		return err
	}
	if err = verifySQLiteFile(ctx, livePath, expectedVersion); err != nil {
		return fmt.Errorf("verify restored sqlite database: %w", err)
	}
	// Verification opens the restored file and recreates an empty WAL; the
	// restored generation must start without any sidecar from either epoch.
	for _, sidecar := range []string{livePath + "-wal", livePath + "-shm"} {
		if err = os.Remove(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove restored sqlite sidecar %s: %w", filepath.Base(sidecar), err)
		}
	}
	return nil
}

func copyFilePrivate(source, target string) error {
	in, err := os.Open(source) // #nosec G304 -- caller passes the root-approved snapshot path.
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 G703 -- private sibling of the live database inside the root-owned data root.
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func verifySQLiteFile(ctx context.Context, path string, expectedVersion int) error {
	db, err := sqlitedb.OpenExisting(ctx, path, expectedVersion)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var integrity string
	if err = db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("integrity_check=%s", integrity)
	}
	var version int
	if err = db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version != expectedVersion {
		return fmt.Errorf("user_version=%d, want %d", version, expectedVersion)
	}
	var foreignKeyViolations int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations); err != nil {
		return err
	}
	if foreignKeyViolations != 0 {
		return fmt.Errorf("foreign_key_check found %d violations", foreignKeyViolations)
	}
	return nil
}

// sqliteRestoreSource adapts the restored single-engine store to the auth
// snapshot builder.
type sqliteRestoreSource struct{ db *sql.DB }

func (s sqliteRestoreSource) Query(ctx context.Context, query string, args ...any) (dovecotauth.Rows, error) {
	return s.db.QueryContext(ctx, query, args...)
}

// restoreSQLiteDatabase replaces the live database from a verified snapshot and
// rebuilds the dovecot auth snapshot before the restore may be released (§9.2).
func restoreSQLiteDatabase(ctx context.Context, cfg *config.Config, snapshotPath string) error {
	if cfg == nil || strings.TrimSpace(cfg.SQLitePath) == "" {
		return errors.New("sqlite restore requires URMAIL_SQLITE_PATH")
	}
	if err := restoreSQLiteSnapshot(ctx, snapshotPath, cfg.SQLitePath, sqlitedb.ExpectedSchemaVersion); err != nil {
		return err
	}
	return rebuildAuthSnapshotAfterRestore(ctx, cfg)
}

// rebuildAuthSnapshotAfterRestore is the §5.4 restore gate: the snapshot must be
// rebuilt from the restored store before auth is released.
func rebuildAuthSnapshotAfterRestore(ctx context.Context, cfg *config.Config) error {
	live, err := sql.Open("sqlite", "file:"+cfg.SQLitePath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open restored sqlite store: %w", err)
	}
	defer func() { _ = live.Close() }()
	manager := dovecotauth.NewManager(cfg.DovecotAuthDB, sqliteRestoreSource{db: live})
	if _, err = dovecotauth.RequireFreshAfterRestore(ctx, manager); err != nil {
		return err
	}
	return nil
}
