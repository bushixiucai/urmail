package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

// TestCreateSQLiteSnapshotWritesConsistentCopy proves the backup artifact is a
// standalone database with the expected schema and the committed rows, and
// that WAL content is folded in before the copy.
func TestCreateSQLiteSnapshotWritesConsistentCopy(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "urmail.sqlite")
	db, err := sqlitedb.Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlitedb.Migrate(ctx, db, migrations_sqlite.FS, "."); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO brands(id,slug,name) VALUES(7,'snap','Snapshot')`); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "snapshot", "urmail-20260101T000000Z.sqlite")
	if err = createSQLiteSnapshot(ctx, source, target); err != nil {
		t.Fatalf("createSQLiteSnapshot: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode=%o, want 0600", info.Mode().Perm())
	}
	snap, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close() }()
	var version, brands int
	if err = snap.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != sqlitedb.ExpectedSchemaVersion {
		t.Fatalf("snapshot user_version=%d err=%v", version, err)
	}
	if err = snap.QueryRow(`SELECT count(*) FROM brands WHERE id=7`).Scan(&brands); err != nil || brands != 1 {
		t.Fatalf("snapshot rows=%d err=%v", brands, err)
	}
	if err = createSQLiteSnapshot(ctx, source, target); err == nil {
		t.Fatal("existing snapshot target was overwritten")
	}
}