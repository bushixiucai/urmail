package backupauto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/testdb"
)

// fakeRunner produces one small plaintext artifact without touching PG or
// the host backup paths.
type fakeRunner struct{ Dir string }

func (r fakeRunner) RunBackup(_ context.Context, stamp string) ([]LocalArtifact, string, bool, error) {
	entries, _ := PlanBackupSet(PlannerConfig{OutDir: r.Dir, Stamp: stamp, IncludeMail: true})
	artifacts := make([]LocalArtifact, 0, len(entries))
	for _, entry := range entries {
		file := filepath.Join(r.Dir, entry.Name)
		data := []byte("explicit synthetic " + entry.Kind + " fixture bytes")
		if err := os.WriteFile(file, data, 0o600); err != nil {
			return nil, "", false, err
		}
		sum := sha256.Sum256(data)
		artifacts = append(artifacts, LocalArtifact{Name: entry.Name, Path: file, Kind: entry.Kind, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	}
	return artifacts, ScopeComplete, true, nil
}

func TestBackupRunsStateTransitions(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	recorder := &StateRecorder{Pool: platform}

	runUID, err := recorder.StartRun(ctx, ScopeComplete)
	if err != nil {
		t.Fatal(err)
	}
	if len(runUID) < 8 {
		t.Fatalf("run uid too short: %q", runUID)
	}

	var state string
	if err := platform.Pool.QueryRow(ctx, "SELECT state FROM backup_runs WHERE run_uid=$1", runUID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != StateRunning {
		t.Fatalf("new run must be running, got %q", state)
	}

	if err := recorder.MarkVerifying(ctx, runUID); err != nil {
		t.Fatal(err)
	}
	if err := recorder.FinishRun(ctx, runUID, true, nil, 1234, 5*time.Second, "urmail-x.manifest.json", "s3://bucket/x", 2); err != nil {
		t.Fatal(err)
	}
	var bytes int64
	var deleted int
	if err := platform.Pool.QueryRow(ctx, "SELECT state,bytes,retention_deleted FROM backup_runs WHERE run_uid=$1", runUID).Scan(&state, &bytes, &deleted); err != nil {
		t.Fatal(err)
	}
	if state != StateSuccess || bytes != 1234 || deleted != 2 {
		t.Fatalf("success row wrong: state=%q bytes=%d deleted=%d", state, bytes, deleted)
	}

	// Failure path records the error text and keeps the row observable.
	failUID, err := recorder.StartRun(ctx, ScopeComplete)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.FinishRun(ctx, failUID, false, context.DeadlineExceeded, 0, time.Second, "", "s3://bucket/x", 0); err != nil {
		t.Fatal(err)
	}
	var errorText string
	if err := platform.Pool.QueryRow(ctx, "SELECT state,error FROM backup_runs WHERE run_uid=$1", failUID).Scan(&state, &errorText); err != nil {
		t.Fatal(err)
	}
	if state != StateFailed || errorText != context.DeadlineExceeded.Error() {
		t.Fatalf("failed row wrong: state=%q error=%q", state, errorText)
	}

	// Stale running rows from a crash are marked failed, never deleted.
	staleUID, err := recorder.StartRun(ctx, ScopeComplete)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Exec(ctx, "UPDATE backup_runs SET started_at=$2 WHERE run_uid=$1", staleUID, time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	resolved, err := recorder.StaleRunning(ctx, 24*time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 1 {
		t.Fatalf("expected one stale run resolved, got %d", resolved)
	}
	if err = platform.Pool.QueryRow(ctx, "SELECT state FROM backup_runs WHERE run_uid=$1", staleUID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != StateFailed {
		t.Fatalf("stale run must become failed, got %q", state)
	}

	// LastRun returns the newest row regardless of state.
	last, err := recorder.LastRun(ctx)
	if err != nil || last == nil {
		t.Fatalf("last run: %v %v", last, err)
	}
}

func TestWorkerFullPipelineRecordsSuccess(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	worker := &Worker{
		Pool:       platform,
		Runner:     fakeRunner{Dir: t.TempDir()},
		Store:      newMemoryStore(),
		Recipient:  identity.Recipient().String(),
		OffsiteURI: "s3://test-bucket/backups",
		Interval:   time.Hour,
		Retention:  RetentionPolicy{KeepDaily: 3, KeepWeekly: 2, KeepMonthly: 1},
		Now:        time.Now,
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("full pipeline: %v", err)
	}
	var state, manifestName string
	if err := platform.Pool.QueryRow(ctx, "SELECT state,manifest_name FROM backup_runs ORDER BY id DESC LIMIT 1").Scan(&state, &manifestName); err != nil {
		t.Fatal(err)
	}
	if state != StateSuccess {
		t.Fatalf("pipeline must record success, got %q", state)
	}
	if manifestName == "" {
		t.Fatal("success row must record the manifest name")
	}

	// A second immediate run must be suppressed by the interval guard.
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var runs int
	if err := platform.Pool.QueryRow(ctx, "SELECT count(*) FROM backup_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("interval guard must suppress the second run, got %d runs", runs)
	}

	// A failing runner records failed state AND raises an outbox notice.
	worker.Interval = time.Nanosecond
	worker.Runner = failingRunner{}
	if err := worker.RunOnce(ctx); err == nil {
		t.Fatal("failing runner must surface an error")
	}
	if err := platform.Pool.QueryRow(ctx, "SELECT state FROM backup_runs ORDER BY id DESC LIMIT 1").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != StateFailed {
		t.Fatalf("failure must be recorded, got %q", state)
	}
	var notices int
	if err := platform.Pool.QueryRow(ctx, "SELECT count(*) FROM notification_outbox WHERE kind=$1", "backup_failed").Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatalf("failure must raise exactly one outbox notice, got %d", notices)
	}

	// The successful generation from the earlier run must still be intact:
	// failures never delete existing backups.
	objects, err := worker.Store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) == 0 {
		t.Fatal("existing offsite backups must survive a failed run")
	}
	_ = zap.NewNop()
}

type failingRunner struct{}

func (failingRunner) RunBackup(context.Context, string) ([]LocalArtifact, string, bool, error) {
	return nil, "", false, errFailingRunner
}

var errFailingRunner = errTestFailure{}

type errTestFailure struct{}

func (errTestFailure) Error() string { return "runner failed on purpose" }

func TestRetentionRefusalPreservesVerifiedBackupAndRecordsPartialDelete(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	worker, store, identity, previousAnchor, previousManifest := retentionWorkerFixture(t)
	delete(store.objects, previousAnchor)
	for _, artifact := range previousManifest.Artifacts {
		delete(store.objects, artifact.EncryptedName)
	}
	_, old, _ := retentionGenerationFixture(t, store, identity.Recipient().String(), worker.now().Add(-72*time.Hour))
	store.failDelete = old.Artifacts[1].EncryptedName
	worker.Pool = platform
	worker.Runner = fakeRunner{Dir: t.TempDir()}
	worker.OffsiteURI = "s3://explicit-isolated-fixture/owned"
	if e := worker.RunOnce(t.Context()); e != nil {
		t.Fatal("verified backup failed because pruning was refused", e)
	}
	var status, note string
	var deleted int
	if e := platform.QueryRow(t.Context(), "SELECT state,error,retention_deleted FROM backup_runs ORDER BY id DESC LIMIT 1").Scan(&status, &note, &deleted); e != nil {
		t.Fatal(e)
	}
	if status != StateSuccess || !strings.Contains(note, "retention skipped") || deleted != 1 {
		t.Fatal("partial retention result not observable", status, note, deleted)
	}
	if _, ok := store.objects[ManifestName(StampFor(worker.now()))]; !ok {
		t.Fatal("new complete backup lost")
	}
}
