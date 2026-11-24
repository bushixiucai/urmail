package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// TestBackupAutoPlatformPoolSQLiteIgnoresPostgresDSN reproduces the run5
// failure: in single-engine mode the platform pool must come from the SQLite
// file, never from the (nonexistent) PostgreSQL DSN, and the
// settings probe must still work.
func TestBackupAutoPlatformPoolSQLiteIgnoresPostgresDSN(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlitePath := buildRestoreFixture(t, dir, "urmail.sqlite", 1)
	cfg := &config.Config{
		DBEngine:            "sqlite",
		SQLitePath:          sqlitePath,
		DatabaseURLPlatform: "postgres://root@/urmail?host=/tmp/.s.PGSQL.5432&sslmode=disable",
	}
	pool, err := openBackupPlatformPool(ctx, cfg)
	if err != nil {
		t.Fatalf("sqlite platform pool: %v", err)
	}
	defer pool.Close()

	// The offsite settings probe runs on the SQLite pool; an unset row means
	// "no destination configured", not a connection failure.
	if _, _, _, disabled, err := offsiteDestinationFromSettings(ctx, cfg, ""); err != nil || !disabled {
		t.Fatalf("settings probe disabled=%v err=%v", disabled, err)
	}

	if filepath.Base(sqlitePath) != "urmail.sqlite" {
		t.Fatalf("fixture path drifted: %s", sqlitePath)
	}
}

// TestStateRecorderSQLiteDialect runs the four bookkeeping methods the
// automatic backup needs on the single engine, including stale reclamation and
// the pending_local_only row the freshness gate reads.
func TestStateRecorderSQLiteDialect(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlitePath := buildRestoreFixture(t, dir, "urmail.sqlite", 1)
	cfg := &config.Config{DBEngine: "sqlite", SQLitePath: sqlitePath, DatabaseURLPlatform: "postgres://root@/urmail?host=/tmp/.s.PGSQL.5432"}
	pool, err := openBackupPlatformPool(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	recorder := &backupauto.StateRecorder{Pool: pool}

	staleUID, err := recorder.StartRun(ctx, backupauto.ScopeComplete, time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("start stale run: %v", err)
	}
	reclaimed, err := recorder.StaleRunning(ctx, time.Hour, time.Now())
	if err != nil || reclaimed != 1 {
		t.Fatalf("stale running reclaimed=%d err=%v", reclaimed, err)
	}
	if health, err := recorder.LastRunHealth(ctx); err != nil || health.RunUID != staleUID || health.State != backupauto.StateFailed {
		t.Fatalf("stale health=%+v err=%v", health, err)
	}

	runUID, err := recorder.StartRun(ctx, backupauto.ScopeComplete, time.Now())
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if err = recorder.SetOffsiteState(ctx, runUID, backupauto.OffsiteStatePendingLocalOnly); err != nil {
		t.Fatalf("offsite state: %v", err)
	}
	if err = recorder.FinishRun(ctx, runUID, true, nil, 4096, time.Second, "urmail-test.manifest.json", "", 0); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	health, err := recorder.LastRunHealth(ctx)
	if err != nil || health.State != backupauto.StateSuccess || health.OffsiteState != backupauto.OffsiteStatePendingLocalOnly {
		t.Fatalf("health=%+v err=%v", health, err)
	}
	if health.StartedAt.IsZero() || time.Since(health.StartedAt) > time.Minute {
		t.Fatalf("started_at not decoded from micros: %v", health.StartedAt)
	}
	if streak, err := recorder.FailureStreak(ctx); err != nil || streak != 0 {
		t.Fatalf("failure streak=%d err=%v", streak, err)
	}
}

func TestStateRecorderSQLiteMissingTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "empty.sqlite")
	raw, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.NewSQLitePool(raw, isolation.ScopePlatform)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = (&backupauto.StateRecorder{Pool: pool}).LastRunHealth(ctx); !errors.Is(err, backupauto.ErrBackupOffsiteDisabled) {
		t.Fatalf("missing table err=%v, want ErrBackupOffsiteDisabled", err)
	}
}

type sqliteRunner struct{ dir, stamp string }

func (r sqliteRunner) RunBackup(_ context.Context, stamp string) ([]backupauto.LocalArtifact, string, bool, error) {
	r.stamp = stamp
	names := []struct {
		file, kind string
	}{
		{"urmail-" + stamp + ".sqlite", backupauto.KindSqliteSnapshot},
		{"urmail-config-" + stamp + ".tar.gz", backupauto.KindConfigTar},
		{"urmail-mail-" + stamp + ".tar.gz", backupauto.KindMailTar},
	}
	var out []backupauto.LocalArtifact
	for _, n := range names {
		path := filepath.Join(r.dir, n.file)
		if err := os.WriteFile(path, []byte("artifact-"+n.kind), 0o600); err != nil {
			return nil, "", false, err
		}
		sum := sha256.Sum256([]byte("artifact-" + n.kind))
		out = append(out, backupauto.LocalArtifact{Name: n.file, Path: path, Kind: n.kind, Size: int64(len("artifact-" + n.kind)), SHA256: hex.EncodeToString(sum[:])})
	}
	return out, backupauto.ScopeComplete, true, nil
}

// TestBackupAutoRunOnceSQLitePendingLocalOnly drives the whole worker on the
// single engine: no PostgreSQL DSN is usable here, so any PG connection
// attempt would fail the run before the row could be recorded.
func TestBackupAutoRunOnceSQLitePendingLocalOnly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlitePath := buildRestoreFixture(t, dir, "urmail.sqlite", 1)
	cfg := &config.Config{DBEngine: "sqlite", SQLitePath: sqlitePath, DatabaseURLPlatform: "postgres://root@/urmail?host=/tmp/.s.PGSQL.5432"}
	pool, err := openBackupPlatformPool(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	spool := t.TempDir()
	worker := &backupauto.Worker{
		Pool:      pool,
		Runner:    sqliteRunner{dir: spool},
		Recorder:  &backupauto.StateRecorder{Pool: pool},
		LocalOnly: true,
		Now:       time.Now,
	}
	if err = worker.RunOnce(ctx); err != nil {
		t.Fatalf("worker RunOnce on sqlite: %v", err)
	}
	health, err := (&backupauto.StateRecorder{Pool: pool}).LastRunHealth(ctx)
	if err != nil || health == nil {
		t.Fatalf("last run health: %v", err)
	}
	if health.State != backupauto.StateSuccess || health.Scope != backupauto.ScopeComplete || health.OffsiteState != backupauto.OffsiteStatePendingLocalOnly {
		t.Fatalf("run state=%q scope=%q offsite=%q", health.State, health.Scope, health.OffsiteState)
	}
}
