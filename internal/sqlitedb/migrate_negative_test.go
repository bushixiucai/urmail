package sqlitedb_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

func openTempSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlitedb.Open(context.Background(), filepath.Join(t.TempDir(), "neg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestMigrateRefusesGapAheadAndMissingTail is the negative matrix used by the
// release gate: a gap in the chain, a database ahead of the binary, and a
// binary whose chain is shorter than the expected schema version all refuse.
func TestMigrateRefusesGapAheadAndMissingTail(t *testing.T) {
	body := &fstest.MapFile{Data: []byte("CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, dirty BOOLEAN NOT NULL DEFAULT 0);\nCREATE TABLE IF NOT EXISTS fixture(a INTEGER);\n")}
	gapFS := fstest.MapFS{"0001_init.sql": body, "0003_gap.sql": body}
	if err := sqlitedb.Migrate(context.Background(), openTempSQLite(t), gapFS, "."); err == nil || !strings.Contains(err.Error(), "gap at version 2") {
		t.Fatalf("gap chain must be refused, got %v", err)
	}

	aheadDB := openTempSQLite(t)
	if _, err := aheadDB.ExecContext(t.Context(), "PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.Migrate(context.Background(), aheadDB, fstest.MapFS{"0001_init.sql": body}, "."); err == nil || !strings.Contains(err.Error(), "ahead of the embedded chain") {
		t.Fatalf("ahead database must be refused, got %v", err)
	}

	// A binary without the tail file cannot reach the expected version; the
	// CLI/startup comparison must catch the short chain.
	shortDB := openTempSQLite(t)
	shortFS := fstest.MapFS{"0001_init.sql": body, "0002_next.sql": body}
	if err := sqlitedb.Migrate(context.Background(), shortDB, shortFS, "."); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := shortDB.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version == sqlitedb.ExpectedSchemaVersion {
		t.Fatalf("short chain unexpectedly reached the expected version %d", version)
	}
}
