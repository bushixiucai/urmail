package datamigrate

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// schemaFakeSource answers the schema-version and recall-fingerprint queries
// used by the staging gates.
type schemaFakeSource struct {
	version int64
	dirty   bool
	noRow   bool
	rows    [][]any
}

func (s *schemaFakeSource) Query(_ context.Context, q string, _ ...any) (Rows, error) {
	switch {
	case strings.Contains(q, "schema_migrations"):
		if s.noRow {
			return &fakeRecallRows{}, nil
		}
		return &fakeRecallRows{rows: [][]any{{s.version, s.dirty}}}, nil
	case strings.Contains(q, "FROM recall_requests ORDER BY id"):
		return &fakeRecallRows{rows: s.rows}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", q)
	}
}

func TestSourceSchemaGate(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		src     *schemaFakeSource
		wantErr string
	}{
		{"supported", &schemaFakeSource{version: 159}, ""},
		{"supported tip (production 0160)", &schemaFakeSource{version: 160}, ""},
		{"old", &schemaFakeSource{version: 158}, "outside the supported range"},
		{"unknown new", &schemaFakeSource{version: 161}, "outside the supported range"},
		{"dirty", &schemaFakeSource{version: 159, dirty: true}, "dirty"},
		{"missing", &schemaFakeSource{noRow: true}, "unknown"},
	}
	for _, tc := range cases {
		err := verifySourceSchema(ctx, tc.src)
		if tc.wantErr == "" && err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("%s: err=%v want %s", tc.name, err, tc.wantErr)
		}
	}
}

func TestSourceFingerprintDetectsDrift(t *testing.T) {
	ctx := context.Background()
	rows := legalRecallRows()
	src := &schemaFakeSource{version: 159, rows: rows}
	before, err := sourceRecallFingerprint(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	after, err := sourceRecallFingerprint(ctx, src)
	if err != nil || after != before {
		t.Fatalf("fingerprint unstable: %v %s %s", err, before, after)
	}
	if err := verifySourceUnchanged(ctx, src, before); err != nil {
		t.Fatal(err)
	}
	src.rows = rows[:len(rows)-1]
	if err := verifySourceUnchanged(ctx, src, before); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("drift not detected: %v", err)
	}
}

func TestRuntimeRecallGatesOnRestoredGuard(t *testing.T) {
	ctx := context.Background()
	db, tm := recallFixture(t)
	tx := beginTx(t, db)
	if _, refusals, err := migrateRecallTable(ctx, fakeRecallSource{rows: legalRecallRows()}, &Target{DB: sqlTxHandle{tx: tx}}, tm, TableInfo{Name: "recall_requests"}); err != nil || len(refusals) != 0 {
		tx.Rollback()
		t.Fatalf("seed import: err=%v refusals=%v", err, refusals)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	beforeCount := int64(len(legalRecallRows()))
	beforeGuard, err := guardFingerprint(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runRuntimeRecallGates(ctx, db)
	if err != nil {
		t.Fatalf("runtime gates: %v", err)
	}
	if report.Skipped != "" {
		t.Fatalf("runtime gates skipped: %s", report.Skipped)
	}
	if report.Checked != 7 {
		t.Fatalf("runtime gates checked=%d want 7", report.Checked)
	}
	var n int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM recall_requests").Scan(&n); err != nil || n != beforeCount {
		t.Fatalf("gates leaked rows: %d want %d err=%v", n, beforeCount, err)
	}
	afterGuard, err := guardFingerprint(ctx, db)
	if err != nil || afterGuard != beforeGuard {
		t.Fatalf("gates changed trigger DDL: %v", err)
	}
}

func TestStagingRefusesExistingTarget(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	target := filepath.Join(dir, "published.sqlite")
	if err := os.WriteFile(target, []byte("published-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing-migrations")
	_, err := ImportRecallStaging(ctx, StagingOptions{
		SourceURL:  "postgres://urmail_migrate:x@127.0.0.1:1/urmail?sslmode=disable",
		TargetPath: target,
		Migrations: os.DirFS(missing),
		TypeMap:    TypeMap{},
		BatchSize:  1,
	})
	if err == nil || !strings.Contains(err.Error(), "refusing existing staging target") {
		t.Fatalf("existing target not refused: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "published-sentinel" {
		t.Fatalf("existing target changed: %q err=%v", data, err)
	}
	assertNoStagingLeftovers(t, dir)
}

func TestStagingLockExcludesConcurrentRun(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	target := filepath.Join(dir, "published.sqlite")
	if err := os.WriteFile(target+".lock", fmt.Appendf(nil, "pid=%d\n", os.Getpid()), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ImportRecallStaging(ctx, StagingOptions{
		SourceURL:  "postgres://urmail_migrate:x@127.0.0.1:1/urmail?sslmode=disable",
		TargetPath: target,
		Migrations: os.DirFS("../../migrations_sqlite"),
		TypeMap:    mustTypeMap(t),
	})
	if err == nil || !strings.Contains(err.Error(), "locked by another run") {
		t.Fatalf("concurrent run not refused: %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target appeared while lock held: %v", statErr)
	}
}

func TestStaleLockAndOrphanStagingAreReclaimed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	target := filepath.Join(dir, "published.sqlite")
	orphan := filepath.Join(dir, ".urmail-s5-recall-orphan.sqlite")
	if err := os.WriteFile(orphan, []byte("dead partial staging"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan+"-wal", []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".lock", fmt.Appendf(nil, "pid=999999 tmp=%s\n", orphan), 0o600); err != nil {
		t.Fatal(err)
	}
	// A dead pid plus the recorded private staging file is reclaimed; the run
	// then proceeds and fails on the unreachable source without creating a
	// target and without leaving its own staging/lock behind.
	_, err := ImportRecallStaging(ctx, StagingOptions{
		SourceURL:  "postgres://urmail_migrate:x@127.0.0.1:1/urmail?sslmode=disable",
		TargetPath: target,
		Migrations: os.DirFS("../../migrations_sqlite"),
		TypeMap:    mustTypeMap(t),
	})
	if err == nil {
		t.Fatal("unreachable source accepted")
	}
	if _, statErr := os.Stat(orphan); !os.IsNotExist(statErr) {
		t.Fatalf("orphan staging not reclaimed: %v", statErr)
	}
	if _, statErr := os.Stat(target + ".lock"); !os.IsNotExist(statErr) {
		t.Fatalf("stale lock not reclaimed: %v", statErr)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target published on failure: %v", statErr)
	}
	assertNoStagingLeftovers(t, dir)
}

func TestStagingUnreachableSourceLeavesNoTarget(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	target := filepath.Join(dir, "published.sqlite")
	_, err := ImportRecallStaging(ctx, StagingOptions{
		SourceURL:  "postgres://urmail_migrate:x@127.0.0.1:1/urmail?sslmode=disable",
		TargetPath: target,
		Migrations: os.DirFS("../../migrations_sqlite"),
		TypeMap:    mustTypeMap(t),
	})
	if err == nil {
		t.Fatal("unreachable source accepted")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target published on failure: %v", statErr)
	}
	assertNoStagingLeftovers(t, dir)
}

func TestStagingRejectsUnknownSourceSchemaBeforeWrites(t *testing.T) {
	for _, dsn := range []string{
		"postgres://urmail_app:x@127.0.0.1:5432/urmail?sslmode=disable",
		"postgres://urmail_migrate:x@external.example:5432/urmail?sslmode=disable",
		"postgres://urmail_migrate:x@127.0.0.1:5432/urmail?options=-crole%3Dpostgres",
	} {
		if err := ValidateStagingSourceURL(dsn); err == nil {
			t.Fatalf("source URL accepted: %s", dsn)
		}
	}
}

func mustTypeMap(t *testing.T) TypeMap {
	t.Helper()
	tm, err := LoadTypeMap("../../migrations_sqlite/type-map.tsv")
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func assertNoStagingLeftovers(t *testing.T, dir string) {
	t.Helper()
	for _, pattern := range []string{".urmail-s5-recall-*", "*.lock"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("staging leftovers %s: %v", pattern, matches)
		}
	}
}

// TestSnapshotReadOnlyRepeatable proves the source handle used by the staging
// import is one read-only REPEATABLE READ transaction: a concurrent committed
// insert is invisible to every later read on the snapshot while a fresh
// connection sees it.
func TestSnapshotReadOnlyRepeatable(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	migrator, err := db.NewMigrator(mustMigrateURL(t, dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrator.Close() }()
	if err := migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}
	src, closer, err := OpenPGSource(ctx, dsn)
	if err != nil {
		t.Fatalf("open snapshot source: %v", err)
	}
	defer closer()
	var report StagingReport
	if err := verifyReadSnapshot(ctx, src, &report); err != nil {
		t.Fatalf("read snapshot contract: %v", err)
	}
	if report.SnapshotIsolation != "repeatable read" || report.SnapshotReadOnly != "on" {
		t.Fatalf("snapshot=%q read_only=%q", report.SnapshotIsolation, report.SnapshotReadOnly)
	}
	count := func(s Source) int64 {
		rows, err := s.Query(ctx, "SELECT count(*) FROM brands")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var n int64
		if !rows.Next() {
			t.Fatal("no row")
		}
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count(src)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "INSERT INTO brands(id,slug,name) VALUES(900,'snapshot-concurrent','S')"); err != nil {
		t.Fatalf("concurrent insert: %v", err)
	}
	if got := count(src); got != before {
		t.Fatalf("snapshot saw a concurrent commit: %d -> %d (generation mixing)", before, got)
	}
	fresh, freshCloser, err := OpenPGSource(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer freshCloser()
	if got := count(fresh); got <= before {
		t.Fatalf("fresh connection did not see the committed row: %d", got)
	}
}

func mustMigrateURL(t *testing.T, dsn string) string {
	t.Helper()
	return dsn
}
