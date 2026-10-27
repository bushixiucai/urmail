package backupauto

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// TestRunHealthStaleThresholds is pure: a run counts as stuck only while it
// claims to be running/verifying past the crash threshold, so readiness can
// distinguish "worker is mid-run" from "worker died and nothing will resolve
// the row".
func TestRunHealthStaleThresholds(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name       string
		state      string
		startedAgo time.Duration
		want       bool
	}{
		{"fresh running", StateRunning, time.Hour, false},
		{"fresh verifying", StateVerifying, time.Hour, false},
		{"abandoned running", StateRunning, staleRunThreshold + time.Minute, true},
		{"abandoned verifying", StateVerifying, staleRunThreshold + time.Minute, true},
		{"old success is finished", StateSuccess, staleRunThreshold + time.Minute, false},
		{"old failure is finished", StateFailed, staleRunThreshold + time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &RunHealth{State: tc.state, StartedAt: now.Add(-tc.startedAgo)}
			if got := h.StaleRun(now); got != tc.want {
				t.Fatalf("StaleRun=%v want %v", got, tc.want)
			}
		})
	}
	var nilHealth *RunHealth
	if nilHealth.StaleRun(now) {
		t.Fatal("nil health must not be stale")
	}
}

// TestLastRunHealthAgainstRealTable exercises the disabled, empty and
// populated states of backup_runs on a real PostgreSQL fixture.
func TestLastRunHealthAgainstRealTable(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	recorder := &StateRecorder{Pool: platform}

	health, err := recorder.LastRunHealth(ctx)
	if !errors.Is(err, ErrNoRuns) || health != nil {
		t.Fatalf("empty table must read as no run yet, got %+v %v", health, err)
	}

	if _, err = recorder.StartRun(ctx, ScopeComplete); err != nil {
		t.Fatal(err)
	}
	uid, err := recorder.StartRun(ctx, ScopeComplete)
	if err != nil {
		t.Fatal(err)
	}
	// The most recent run wins: finish only the second one as failed and the
	// probe summary must show its error text, not the older running row.
	finishErr := errors.New("upload denied by destination")
	if err = recorder.FinishRun(ctx, uid, false, finishErr, 10, time.Minute, "urmail-x.manifest.json", "s3://offsite/test", 0); err != nil {
		t.Fatal(err)
	}
	health, err = recorder.LastRunHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.State != StateFailed || health.Error != finishErr.Error() || health.RunUID != uid {
		t.Fatalf("failed run summary wrong: %+v", health)
	}

	// A later successful run clears the failure for readiness purposes.
	if _, err = recorder.StartRun(ctx, ScopeComplete); err != nil {
		t.Fatal(err)
	}
	latest, err := recorder.LastRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = recorder.FinishRun(ctx, latest.RunUID, true, nil, 20, 2*time.Minute, "urmail-y.manifest.json", "s3://offsite/test", 0); err != nil {
		t.Fatal(err)
	}
	if health, err = recorder.LastRunHealth(ctx); err != nil || health.State != StateSuccess {
		t.Fatalf("success run not surfaced: %+v %v", health, err)
	}
	if health.StaleRun(time.Now()) {
		t.Fatal("finished run must never be stale")
	}
}

// TestLastRunHealthWithoutBackupRunsTable proves the pre-0120 state reads as
// an explicit "disabled" instead of an error: migration 0120 down removes the
// table and the probe must treat that installation as feature-absent.
func TestLastRunHealthWithoutBackupRunsTable(t *testing.T) {
	dsn := testdb.New(t)
	testdb.MigrateTo(t, dsn, 119) // one version before 0120_backup_runs
	platform, err := db.OpenPlatform(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(platform.Close)
	recorder := &StateRecorder{Pool: platform}
	health, err := recorder.LastRunHealth(context.Background())
	if !errors.Is(err, ErrBackupOffsiteDisabled) {
		t.Fatalf("missing backup_runs must be ErrBackupOffsiteDisabled, got %+v %v", health, err)
	}
	if health != nil {
		t.Fatalf("disabled feature must not return health: %+v", health)
	}
}
