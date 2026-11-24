package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bushixiucai/urmail/internal/config"
)

// backupDatabaseArtifact produces exactly one database artifact for the run:
// a VACUUM INTO snapshot in SQLite single-engine mode, or the PostgreSQL dump
// otherwise. The engines are mutually exclusive by construction: the SQLite
// branch never reaches the pg_dump command builders, and the PostgreSQL branch
// is unreachable when cfg.IsSQLite() is true.
func backupDatabaseArtifact(ctx context.Context, cfg *config.Config, outDir, stamp string) (string, error) {
	if cfg.IsSQLite() {
		// Under --maintenance the writers are already fenced; the checkpoint
		// folds any WAL content into the main file before VACUUM INTO reads it.
		dump := filepath.Join(outDir, "urmail-"+stamp+".sqlite")
		if err := createSQLiteSnapshot(ctx, cfg.SQLitePath, dump); err != nil {
			return "", err
		}
		return dump, nil
	}
	dbURL, err := backupPeerDatabaseURL(cfg.DatabaseURLMigrate)
	if err != nil {
		return "", err
	}
	dump := filepath.Join(outDir, "urmail-"+stamp+".dump")
	cmd, err := backupDatabaseCommand(ctx, dbURL)
	if err != nil {
		return "", err
	}
	dumpFile, err := os.OpenFile(dump, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 G703 -- root-authorized private backup artifact, never opened by the postgres account.
	if err != nil {
		return "", err
	}
	defer func() { _ = dumpFile.Close() }()
	cmd.Stdout = dumpFile
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("database backup failed: %w", err)
	}
	if err = dumpFile.Sync(); err != nil {
		return "", err
	}
	if err = dumpFile.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(dump, 0o600); err != nil { // #nosec G703 -- root-authorized artifact is inside the private backup directory.
		return "", err
	}
	return dump, nil
}