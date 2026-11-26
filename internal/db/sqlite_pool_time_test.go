package db_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

// TestSQLitePoolEpochTimeRoundTrip is the pm-codec-epoch-1 regression: the
// backup_runs started_at/finished_at columns are INTEGER microseconds and the
// QA backup-auto hard gate crashed scanning them into *time.Time. The pool
// must encode time.Time arguments as UnixMicro and decode INTEGER cells back,
// including NULL (zero time) and the epoch boundaries.
func TestSQLitePoolEpochTimeRoundTrip(t *testing.T) {
	ctx := context.Background()
	raw, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "epoch.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlitedb.Migrate(ctx, raw, migrations_sqlite.FS, "."); err != nil {
		t.Fatal(err)
	}
	pool, err := db.NewSQLitePool(raw, isolation.ScopePlatform)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	scoped := isolation.WithScope(ctx, isolation.ScopeContext{Scope: isolation.ScopePlatform})

	started := time.Date(2026, 11, 26, 8, 0, 0, 123000, time.UTC)
	finished := started.Add(90 * time.Second)
	// Write through time.Time arguments (the pool encodes UnixMicro) and read
	// back through *time.Time destinations (the pool decodes the same unit).
	if _, err = pool.Exec(scoped,
		`INSERT INTO backup_runs(run_uid,state,scope,started_at,finished_at) VALUES($1,$2,$3,$4,$5)`,
		"run-roundtrip", "success", "local-mail-and-metadata", started, finished); err != nil {
		t.Fatal(err)
	}
	// A second row with NULL finished_at covers the NULL branch.
	if _, err = raw.ExecContext(ctx,
		`INSERT INTO backup_runs(run_uid,state,scope,started_at) VALUES('run-null01','running','scope',1732608000000000)`); err != nil {
		t.Fatal(err)
	}

	var gotStarted, gotFinished time.Time
	if err = pool.QueryRow(scoped,
		`SELECT started_at,finished_at FROM backup_runs WHERE run_uid='run-roundtrip'`).Scan(&gotStarted, &gotFinished); err != nil {
		t.Fatalf("roundtrip scan: %v", err)
	}
	if !gotStarted.Equal(started) || !gotFinished.Equal(finished) {
		t.Fatalf("roundtrip got started=%v finished=%v want %v/%v", gotStarted, gotFinished, started, finished)
	}

	var nullFinished time.Time
	var nullStarted time.Time
	if err = pool.QueryRow(scoped,
		`SELECT started_at,finished_at FROM backup_runs WHERE run_uid='run-null01'`).Scan(&nullStarted, &nullFinished); err != nil {
		t.Fatalf("null scan: %v", err)
	}
	if !nullFinished.IsZero() {
		t.Fatalf("NULL finished_at scanned as %v, want zero time", nullFinished)
	}
	if got := time.UnixMicro(1732608000000000).UTC(); !nullStarted.Equal(got) {
		t.Fatalf("started_at=%v want %v", nullStarted, got)
	}

	// Boundary values: epoch zero and the far end of the representable range.
	if _, err = raw.ExecContext(ctx,
		`INSERT INTO backup_runs(run_uid,state,scope,started_at) VALUES('run-zero01','failed','scope',0)`); err != nil {
		t.Fatal(err)
	}
	var zero time.Time
	if err = pool.QueryRow(scoped,
		`SELECT started_at FROM backup_runs WHERE run_uid='run-zero01'`).Scan(&zero); err != nil {
		t.Fatal(err)
	}
	if !zero.Equal(time.UnixMicro(0).UTC()) {
		t.Fatalf("epoch 0 scanned as %v", zero)
	}
	far := time.UnixMicro(int64(253402300799999999)).UTC() // 9999-12-31
	if _, err = raw.ExecContext(ctx,
		`INSERT INTO backup_runs(run_uid,state,scope,started_at) VALUES('run-far001','failed','scope',$1)`,
		far.UnixMicro()); err != nil {
		t.Fatal(err)
	}
	var gotFar time.Time
	if err = pool.QueryRow(scoped,
		`SELECT started_at FROM backup_runs WHERE run_uid='run-far001'`).Scan(&gotFar); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotFar, far) {
		t.Fatalf("far epoch scanned as %v want %v", gotFar, far)
	}

	// Writing a time.Time argument must land as INTEGER microseconds so the
	// SQL-side epoch arithmetic (duration_ms=($1-started_at)/1000) keeps
	// working and no driver text format leaks into the column.
	if _, err = pool.Exec(scoped,
		`INSERT INTO backup_runs(run_uid,state,scope,started_at) VALUES($1,$2,$3,$4)`,
		"run-arg001", "running", "scope", started); err != nil {
		t.Fatal(err)
	}
	var storedMicros int64
	if err = raw.QueryRowContext(ctx,
		`SELECT started_at FROM backup_runs WHERE run_uid='run-arg001'`).Scan(&storedMicros); err != nil {
		t.Fatal(err)
	}
	if storedMicros != started.UnixMicro() {
		t.Fatalf("time.Time arg stored as %d, want %d", storedMicros, started.UnixMicro())
	}
}
