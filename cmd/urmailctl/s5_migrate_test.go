package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestSQLiteImportCLIValidation(t *testing.T) {
	ctx := t.Context()

	if err := runSQLiteImport(ctx, []string{}, false, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "absolute --target") {
		t.Fatalf("expected missing target rejection, got %v", err)
	}
	if err := runSQLiteImport(ctx, []string{"--target", "relative.sqlite"}, false, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "absolute --target") {
		t.Fatalf("expected relative target rejection, got %v", err)
	}
	target := filepath.Join(t.TempDir(), "out.sqlite")
	t.Setenv("URMAIL_TEST_EMPTY_MIGRATE_URL", "")
	if err := runSQLiteImport(ctx, []string{"--target", target, "--source-env", "URMAIL_TEST_EMPTY_MIGRATE_URL"}, false, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty source-env rejection, got %v", err)
	}
}

func TestSQLiteImportCLIZeroDiffAgainstPG(t *testing.T) {
	if os.Getenv("URMAIL_TEST_DATABASE_URL") == "" {
		t.Skip("URMAIL_TEST_DATABASE_URL not set; skipping PG->SQLite CLI 0-diff test")
	}
	ctx := t.Context()
	dsn := testdb.New(t)
	migrator, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("migrate PG fixture: %v", err)
	}
	_ = migrator.Close()

	t.Setenv("URMAIL_S5_CLI_SOURCE_URL", dsn)
	target := filepath.Join(t.TempDir(), "s5-cli-target.sqlite")

	var out bytes.Buffer
	if err := runSQLiteImport(ctx, []string{"--target", target, "--source-env", "URMAIL_S5_CLI_SOURCE_URL"}, false, &out); err != nil {
		t.Fatalf("runSQLiteImport: %v", err)
	}
	var rep sqliteImportReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("decode report: %v (%s)", err, out.String())
	}
	if !rep.ZeroDiff || !rep.RowHashZeroDiff || rep.SchemaDiffCount != 0 || rep.IndexDiffCount != 0 || rep.ForeignKeyIssues != 0 || rep.IntegrityCheck != "ok" {
		t.Fatalf("expected 0-diff report, got %+v", rep)
	}

	// Verify-only subcommand must also pass 0-diff against the imported target.
	out.Reset()
	if err := runSQLiteImport(ctx, []string{"--target", target, "--source-env", "URMAIL_S5_CLI_SOURCE_URL"}, true, &out); err != nil {
		t.Fatalf("verify-import: %v", err)
	}
	var verifyRep sqliteImportReport
	if err := json.Unmarshal(out.Bytes(), &verifyRep); err != nil {
		t.Fatalf("decode verify report: %v", err)
	}
	if verifyRep.Mode != "verify-only" || !verifyRep.ZeroDiff {
		t.Fatalf("expected verify-only 0-diff, got %+v", verifyRep)
	}
}
