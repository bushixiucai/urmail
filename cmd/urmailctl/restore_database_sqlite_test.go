package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

func buildRestoreFixture(t *testing.T, dir, name string, brands ...int64) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(dir, name)
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlitedb.Migrate(ctx, db, migrations_sqlite.FS, "."); err != nil {
		t.Fatal(err)
	}
	for i, id := range brands {
		if _, err = db.ExecContext(ctx, `INSERT INTO brands(id,slug,name) VALUES(?,?,?)`, id, "brand-"+string(rune('a'+i)), "Brand"); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRestoreSQLiteSnapshotReplacesMainAndDropsWAL(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	snapshot := buildRestoreFixture(t, dir, "snapshot.sqlite", 1)
	live := buildRestoreFixture(t, dir, "live.sqlite", 2)
	// Leave a real WAL beside the live database.
	if _, err := os.Stat(live + "-wal"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := restoreSQLiteSnapshot(ctx, snapshot, live, sqlitedb.ExpectedSchemaVersion); err != nil {
		t.Fatalf("restoreSQLiteSnapshot: %v", err)
	}
	// The restored generation must start without a sidecar from either epoch;
	// check before opening the file again (opening recreates an empty WAL).
	for _, sidecar := range []string{live + "-wal", live + "-shm", live + ".restore-tmp"} {
		if _, err := os.Stat(sidecar); err == nil {
			t.Fatalf("sidecar left behind: %s", filepath.Base(sidecar))
		}
	}
	db, err := sqlitedb.OpenExisting(ctx, live, sqlitedb.ExpectedSchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var restored, stale int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM brands WHERE id=1`).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM brands WHERE id=2`).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if restored != 1 || stale != 0 {
		t.Fatalf("restored=%d stale=%d, want 1/0", restored, stale)
	}
}

func TestRestoreSQLiteSnapshotRefusesWrongVersionAndCorruption(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := buildRestoreFixture(t, dir, "live.sqlite", 1)
	snapshot := buildRestoreFixture(t, dir, "snapshot.sqlite", 2)
	wrong := filepath.Join(dir, "wrong.sqlite")
	db, err := sqlitedb.Open(ctx, wrong)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `PRAGMA user_version=6`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = restoreSQLiteSnapshot(ctx, wrong, live, sqlitedb.ExpectedSchemaVersion); err == nil {
		t.Fatal("snapshot with the wrong user_version was restored")
	}
	corrupt := filepath.Join(dir, "corrupt.sqlite")
	if err = os.WriteFile(corrupt, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = restoreSQLiteSnapshot(ctx, corrupt, live, sqlitedb.ExpectedSchemaVersion); err == nil {
		t.Fatal("corrupt snapshot was restored")
	}
	if err = restoreSQLiteSnapshot(ctx, snapshot, live, sqlitedb.ExpectedSchemaVersion); err != nil {
		t.Fatalf("valid snapshot after failed attempts: %v", err)
	}
}
// TestEndToEndSQLiteBackupThenRestore exercises the product pair: snapshot the
// live database, change it, restore the snapshot, and compare the rows.
func TestEndToEndSQLiteBackupThenRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := buildRestoreFixture(t, dir, "urmail.sqlite", 1)
	snapshot := filepath.Join(dir, "urmail-20260101T000000Z.sqlite")
	if err := createSQLiteSnapshot(ctx, live, snapshot); err != nil {
		t.Fatalf("backup: %v", err)
	}
	db, err := sqlitedb.OpenExisting(ctx, live, sqlitedb.ExpectedSchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO brands(id,slug,name) VALUES(9,'later','Later')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM brands WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = restoreSQLiteSnapshot(ctx, snapshot, live, sqlitedb.ExpectedSchemaVersion); err != nil {
		t.Fatalf("restore: %v", err)
	}
	db, err = sqlitedb.OpenExisting(ctx, live, sqlitedb.ExpectedSchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var original, later int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM brands WHERE id=1`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM brands WHERE id=9`).Scan(&later); err != nil {
		t.Fatal(err)
	}
	if original != 1 || later != 0 {
		t.Fatalf("after restore original=%d later=%d, want 1/0", original, later)
	}
}
