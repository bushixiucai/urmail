package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

// seedSQLite seeds one migrated database with a brand marker and returns it.
func seedSQLite(t *testing.T, path, marker string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlitedb.Migrate(ctx, db, migrations_sqlite.FS, "."); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO brands(slug,name) VALUES(?,?)`, marker, marker); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db
}

// TestSQLiteRestoreRebuildsAuthSnapshot is the §9.2 chain: a verified snapshot
// replaces the live database, the sidecars are folded, and the dovecot auth
// snapshot is rebuilt from the restored store before the restore may release.
func TestSQLiteRestoreRebuildsAuthSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	livePath := filepath.Join(dir, "live.sqlite")
	sourcePath := filepath.Join(dir, "source.sqlite")
	snapshotPath := filepath.Join(dir, "urmail-20261125T000000Z.sqlite")
	authPath := filepath.Join(dir, "dovecot-auth.db")

	live := seedSQLite(t, livePath, "live-before-restore")
	defer func() { _ = live.Close() }()
	source := seedSQLite(t, sourcePath, "from-snapshot")
	if _, err := source.ExecContext(ctx, `VACUUM INTO ?`, snapshotPath); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{DBEngine: "sqlite", SQLitePath: livePath, DovecotAuthDB: authPath}
	if err := restoreSQLiteDatabase(ctx, cfg, snapshotPath); err != nil {
		t.Fatalf("sqlite restore chain: %v", err)
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		matches, globErr := filepath.Glob(livePath + suffix)
		if globErr != nil {
			t.Fatal(globErr)
		}
		if len(matches) != 0 {
			t.Fatalf("restore left a %s sidecar behind: %v", suffix, matches)
		}
	}
	restored, err := sql.Open("sqlite", "file:"+livePath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	var count int
	if err = restored.QueryRowContext(ctx, `SELECT count(*) FROM brands WHERE slug='from-snapshot'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restored brand count=%d err=%v", count, err)
	}
	if err = restored.QueryRowContext(ctx, `SELECT count(*) FROM brands WHERE slug='live-before-restore'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pre-restore generation survived: count=%d err=%v", count, err)
	}
	var version int
	if err = restored.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != sqlitedb.ExpectedSchemaVersion {
		t.Fatalf("restored user_version=%d err=%v", version, err)
	}

	// The restore gate rebuilt the auth snapshot from the restored store.
	auth, err := sql.Open("sqlite", "file:"+authPath)
	if err != nil {
		t.Fatalf("auth snapshot missing after restore: %v", err)
	}
	defer func() { _ = auth.Close() }()
	var authVersion, tenants int
	if err = auth.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&authVersion); err != nil || authVersion != 1 {
		t.Fatalf("auth snapshot user_version=%d err=%v", authVersion, err)
	}
	if err = auth.QueryRowContext(ctx, `SELECT count(*) FROM tenants`).Scan(&tenants); err != nil {
		t.Fatalf("auth snapshot unreadable: %v", err)
	}
}

// TestSQLiteRestoreRefusesMissingLivePath keeps the refusal before any file is
// replaced.
func TestSQLiteRestoreRefusesMissingLivePath(t *testing.T) {
	err := restoreSQLiteDatabase(context.Background(), &config.Config{DBEngine: "sqlite"}, "/tmp/snapshot.sqlite")
	if err == nil {
		t.Fatal("restore without a live path was accepted")
	}
}
