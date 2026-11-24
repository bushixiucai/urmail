package main

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

func openV6SQLite(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v6.db")
	db, err := sqlitedb.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"0001_init.sql", "0002_write_side_effects.sql", "0003_mailbox_readonly.sql",
		"0004_archive_trigram_fts.sql", "0005_archive_bigram_fts.sql", "0006_domain_tls_period_check.sql",
	} {
		body, readErr := fs.ReadFile(migrations_sqlite.FS, name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := db.Exec(string(body)); execErr != nil {
			t.Fatalf("apply %s: %v", name, execErr)
		}
	}
	if _, err = db.Exec("PRAGMA user_version=6"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO backup_runs(run_uid,state,scope) VALUES('cli-v6-0001','success','local-mail-and-metadata')"); err != nil {
		t.Fatal(err)
	}
	return path, db
}

func TestSQLiteMigrateUpV6ToV7(t *testing.T) {
	path, db := openV6SQLite(t)
	defer func() { _ = db.Close() }()
	_ = db.Close()
	t.Setenv("URMAIL_SQLITE_PATH", path)
	out := captureStdout(t, func() {
		if err := runSQLiteMigrate(context.Background(), []string{"migrate", "up"}); err != nil {
			t.Fatalf("sqlite migrate up: %v", err)
		}
	})
	if !strings.Contains(out, "sqlite_migrate_ok user_version=7 expected=7") {
		t.Fatalf("output = %q", out)
	}
	reopened, err := sqlitedb.OpenExisting(context.Background(), path, sqlitedb.ExpectedSchemaVersion)
	if err != nil {
		t.Fatalf("OpenExisting: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	var state string
	if err = reopened.QueryRow("SELECT offsite_state FROM backup_runs WHERE run_uid='cli-v6-0001'").Scan(&state); err != nil || state != "" {
		t.Fatalf("history state=%q err=%v", state, err)
	}
}

func TestSQLiteMigrateRefusesAheadOrBadUsage(t *testing.T) {
	path, db := openV6SQLite(t)
	if _, err := db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	t.Setenv("URMAIL_SQLITE_PATH", path)
	if err := runSQLiteMigrate(context.Background(), []string{"migrate", "up"}); err == nil || !strings.Contains(err.Error(), "ahead") {
		t.Fatalf("ahead database must be refused, got %v", err)
	}
	if err := runSQLiteMigrate(context.Background(), []string{"migrate"}); err == nil {
		t.Fatal("bad usage accepted")
	}
	if err := runSQLiteMigrate(context.Background(), []string{"down", "up"}); err == nil {
		t.Fatal("unsupported direction accepted")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatal(statErr)
	}
}
