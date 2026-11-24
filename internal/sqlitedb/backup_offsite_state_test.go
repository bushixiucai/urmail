package sqlitedb_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// TestBackupOffsiteStateUpgradesExistingV6 proves the incremental 0007
// migration on a database that already carries data: user_version moves to
// the chain tip, the historical row keeps an empty offsite_state, an invalid
// value is rejected by the CHECK, and foreign keys stay clean.
func TestBackupOffsiteStateUpgradesExistingV6(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v6.db")
	db, err := sqlitedb.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Build an installed v6 database from 0001..0006.
	for _, name := range []string{
		"0001_init.sql", "0002_write_side_effects.sql", "0003_mailbox_readonly.sql",
		"0004_archive_trigram_fts.sql", "0005_archive_bigram_fts.sql", "0006_domain_tls_period_check.sql",
	} {
		body, readErr := os.ReadFile(filepath.Join("..", "..", "migrations_sqlite", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := db.ExecContext(t.Context(), string(body)); execErr != nil {
			t.Fatalf("apply %s: %v", name, execErr)
		}
	}
	if _, err = db.ExecContext(t.Context(), "PRAGMA user_version=6"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), "INSERT INTO backup_runs(run_uid,state,scope) VALUES('v6-history-01','success','local-mail-and-metadata')"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), "ALTER TABLE backup_runs DROP COLUMN offsite_state"); err == nil {
		t.Fatal("v6 fixture unexpectedly already has offsite_state")
	}

	migrations := os.DirFS(filepath.Join("..", "..", "migrations_sqlite"))
	if err = sqlitedb.Migrate(context.Background(), db, migrations, "."); err != nil {
		t.Fatalf("migrate v6->chain: %v", err)
	}
	var version int
	if err = db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != sqlitedb.ExpectedSchemaVersion {
		t.Fatalf("user_version=%d err=%v want %d", version, err, sqlitedb.ExpectedSchemaVersion)
	}
	var state string
	if err = db.QueryRowContext(t.Context(), "SELECT offsite_state FROM backup_runs WHERE run_uid='v6-history-01'").Scan(&state); err != nil || state != "" {
		t.Fatalf("historical offsite_state=%q err=%v", state, err)
	}
	if _, err = db.ExecContext(t.Context(), "UPDATE backup_runs SET offsite_state='bogus'"); err == nil {
		t.Fatal("invalid offsite_state accepted")
	}
	fkRows, err := db.QueryContext(t.Context(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fkRows.Close() }()
	if fkRows.Next() {
		t.Fatal("foreign_key_check reported a violation")
	}
	_ = fkRows.Close()
	// A second migration is a no-op at the chain tip.
	if err = sqlitedb.Migrate(context.Background(), db, migrations, "."); err != nil {
		t.Fatalf("migrate at the chain tip: %v", err)
	}
	if err = db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != sqlitedb.ExpectedSchemaVersion {
		t.Fatalf("rerun user_version=%d err=%v", version, err)
	}
	// The startup verification accepts the upgraded database.
	checked, err := sqlitedb.OpenExisting(context.Background(), path, sqlitedb.ExpectedSchemaVersion)
	if err != nil {
		t.Fatalf("OpenExisting: %v", err)
	}
	_ = checked.Close()
}
