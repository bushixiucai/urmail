package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

// defaultSQLitePath matches the compose mount used by the entrypoint.
const defaultSQLitePath = "/srv/urmail/data/db/urmail.sqlite"

// runSQLiteMigrate is the single SQLite migration entry point. It is a thin
// wrapper over sqlitedb.Migrate (the authority for the chain and transaction
// semantics) and reports the migrator's expected version so the caller can
// compare it with the database it actually has.
func runSQLiteMigrate(ctx context.Context, args []string) error {
	if len(args) != 2 || args[0] != "migrate" || args[1] != "up" {
		return fmt.Errorf("usage: urmailctl sqlite migrate up")
	}
	path := os.Getenv("URMAIL_SQLITE_PATH")
	if path == "" {
		path = defaultSQLitePath
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create sqlite directory: %w", err)
	}
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err = sqlitedb.Migrate(ctx, db, migrations_sqlite.FS, "."); err != nil {
		return err
	}
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read sqlite user_version: %w", err)
	}
	expected := sqlitedb.ExpectedSchemaVersion
	if version != expected {
		return fmt.Errorf("sqlite user_version = %d, want %d", version, expected)
	}
	fmt.Printf("sqlite_migrate_ok user_version=%d expected=%d\n", version, expected)
	return nil
}
