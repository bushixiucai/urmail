package backupauto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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

type advancingRunner struct {
	fakeRunner
	advance func()
}

func (r advancingRunner) RunBackup(ctx context.Context, stamp string) ([]LocalArtifact, string, bool, error) {
	artifacts, scope, consistent, err := r.fakeRunner.RunBackup(ctx, stamp)
	r.advance()
	return artifacts, scope, consistent, err
}

func TestWorkerCrossSecondRecordsOffsiteManifest(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Second)
	current := start
	worker := &Worker{
		Pool: platform,
		Runner: advancingRunner{fakeRunner: fakeRunner{Dir: t.TempDir()}, advance: func() {
			current = start.Add(2 * time.Second)
		}},
		Store: newMemoryStore(), Recipient: identity.Recipient().String(),
		OffsiteURI: "s3://test-bucket/backups", Now: func() time.Time { return current },
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var uid, state, manifestName, runError string
	if err := platform.Pool.QueryRow(ctx, "SELECT run_uid,state,manifest_name,COALESCE(error,'') FROM backup_runs").Scan(&uid, &state, &manifestName, &runError); err != nil {
		t.Fatal(err)
	}
	if state != StateSuccess || manifestName != ManifestName(StampFor(start)) {
		t.Fatalf("cross-second backup: run_uid=%s state=%s manifest=%q error=%q", uid, state, manifestName, runError)
	}
}

func TestBackupRunsStateTransitions(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	recorder := &StateRecorder{Pool: platform}

	runUID, err := recorder.StartRun(ctx, ScopeComplete, time.Now())
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
	failUID, err := recorder.StartRun(ctx, ScopeComplete, time.Now())
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
	staleUID, err := recorder.StartRun(ctx, ScopeComplete, time.Now())
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
	var state, manifestName, runUID, runError string
	if err := platform.Pool.QueryRow(ctx, "SELECT run_uid,state,manifest_name,COALESCE(error,'') FROM backup_runs ORDER BY id DESC LIMIT 1").Scan(&runUID, &state, &manifestName, &runError); err != nil {
		t.Fatal(err)
	}
	var initialRuns int
	if err := platform.Pool.QueryRow(ctx, "SELECT count(*) FROM backup_runs").Scan(&initialRuns); err != nil || initialRuns != 1 {
		t.Fatalf("expected exactly this run; count=%d err=%v", initialRuns, err)
	}
	if state != StateSuccess {
		t.Fatalf("pipeline must record success, got %q; run_uid=%s error=%q", state, runUID, runError)
	}
	if manifestName == "" {
		t.Fatalf("success row must record the manifest name; run_uid=%s error=%q", runUID, runError)
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

// failingStore injects an offsite transport outage: every write fails.
type failingStore struct{}

func (failingStore) Upload(context.Context, string, io.Reader, int64) error {
	return fmt.Errorf("offsite unreachable (injected)")
}

func (failingStore) List(context.Context) ([]RemoteObject, error) {
	return nil, fmt.Errorf("offsite unreachable (injected)")
}

func (failingStore) Download(context.Context, string, io.Writer) error {
	return fmt.Errorf("offsite unreachable (injected)")
}

func (failingStore) Delete(context.Context, string) error {
	return fmt.Errorf("offsite unreachable (injected)")
}

// TestWorkerOffsiteFailureMarksRunFailed is the v0.6.25 contract: an
// offsite destination is configured, so a failed offsite copy can never be
// recorded as a success -- otherwise the "fresh, fully verified backup"
// release gate passes on a generation that does not exist offsite. The run
// row must be failed with the offsite cause in its error text, retention
// stays refused (fail-closed: nothing may be deleted when the new
// generation is not verified offsite) and the operator is alerted through
// the backup_offsite_failed outbox row -- without a second generic
// backup_failed row for the same failure (B3).
func TestWorkerOffsiteFailureMarksRunFailed(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	// The clock is pinned so the B1 backoff pacing is asserted exactly.
	current := time.Now().UTC().Truncate(time.Second)
	worker := &Worker{
		Pool:       platform,
		Runner:     fakeRunner{Dir: t.TempDir()},
		Store:      failingStore{},
		Recipient:  identity.Recipient().String(),
		OffsiteURI: "s3://test-bucket/backups",
		Interval:   time.Hour,
		Now:        func() time.Time { return current },
	}
	if err := worker.RunOnce(ctx); err == nil {
		t.Fatal("offsite failure must fail the run")
	}
	var state, note string
	if err := platform.Pool.QueryRow(ctx,
		"SELECT state,error FROM backup_runs ORDER BY id DESC LIMIT 1").Scan(&state, &note); err != nil {
		t.Fatal(err)
	}
	if state != StateFailed {
		t.Fatalf("offsite failure must record a failed run, got %q", state)
	}
	if !strings.Contains(note, "offsite copy failed") || strings.Contains(note, "offsite skipped") {
		t.Fatalf("run error must name the offsite failure, got %q", note)
	}
	if !strings.Contains(note, "MiB in") {
		t.Fatalf("run error must report the kept generation's disk usage, got %q", note)
	}
	var offsiteNotices int
	if err := platform.Pool.QueryRow(ctx,
		"SELECT count(*) FROM notification_outbox WHERE kind=$1", "backup_offsite_failed").Scan(&offsiteNotices); err != nil {
		t.Fatal(err)
	}
	if offsiteNotices != 1 {
		t.Fatalf("expected one backup_offsite_failed alert, got %d", offsiteNotices)
	}
	var failedNotices int
	if err := platform.Pool.QueryRow(ctx,
		"SELECT count(*) FROM notification_outbox WHERE kind=$1", "backup_failed").Scan(&failedNotices); err != nil {
		t.Fatal(err)
	}
	if failedNotices != 0 {
		t.Fatalf("offsite failure must not double-raise backup_failed, got %d", failedNotices)
	}

	// B1: one failure back off one hour -- an immediate retry is suppressed
	// and the failure is never counted as a completed generation.
	current = current.Add(10 * time.Minute)
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("suppressed retry must return nil, got %v", err)
	}
	var runs int
	if err := platform.Pool.QueryRow(ctx, "SELECT count(*) FROM backup_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("backoff must suppress the 10-minute retry, got %d runs", runs)
	}

	// Past the one-hour backoff the retry runs and fails again (streak 2).
	current = current.Add(55 * time.Minute)
	if err := worker.RunOnce(ctx); err == nil {
		t.Fatal("second offsite failure must fail the run too")
	}
	if err := platform.Pool.QueryRow(ctx, "SELECT count(*) FROM backup_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Fatalf("expected the backoff-spaced retry to run, got %d runs", runs)
	}

	// Streak 2 back off two hours: one hour later still suppressed.
	current = current.Add(time.Hour)
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("suppressed retry must return nil, got %v", err)
	}
	if err := platform.Pool.QueryRow(ctx, "SELECT count(*) FROM backup_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Fatalf("streak-2 backoff must suppress the 1h retry, got %d runs", runs)
	}

	// Recovery after the full two hours: the run verifies, records success
	// and resets the failure streak.
	worker.Store = newMemoryStore()
	current = current.Add(2*time.Hour + time.Minute)
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := platform.Pool.QueryRow(ctx, "SELECT state FROM backup_runs ORDER BY id DESC LIMIT 1").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != StateSuccess {
		t.Fatalf("recovered run must record success, got %q", state)
	}
	if err := platform.Pool.QueryRow(ctx,
		"SELECT COALESCE(error,'') FROM backup_runs ORDER BY id DESC LIMIT 1").Scan(&note); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(note, "offsite copy failed") {
		t.Fatalf("recovered run must not carry the failure note, got %q", note)
	}
	streak, err := (&StateRecorder{Pool: platform}).FailureStreak(ctx)
	if err != nil || streak != 0 {
		t.Fatalf("success must reset the failure streak, got %d err=%v", streak, err)
	}
}

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

func TestInterruptedRootBackupCanRetryWithoutWaitingAnEntireSuccessInterval(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	recorder := &StateRecorder{Pool: platform}
	id, err := recorder.StartRun(t.Context(), ScopeComplete, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// The root CLI owns its exclusive operation flock before reclassifying this
	// abandoned row; artifact files remain untouched.
	if _, err = recorder.StaleRunning(t.Context(), 0, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	last, err := recorder.LastRun(t.Context())
	if err != nil || last.RunUID != id || last.State != StateFailed {
		t.Fatal("interrupted run fact missing", last, err)
	}
	// v0.6.25 B1: an interrupted run joins the failure backoff, so the
	// retry is paced at one hour -- still far short of the 24h success
	// interval the abandoned row must never impose.
	worker := &Worker{Pool: platform, Interval: 24 * time.Hour}
	due, err := worker.due(t.Context(), recorder)
	if err != nil || due {
		t.Fatal("interrupted run must enter the failure backoff first", due, err)
	}
	if _, err := platform.Exec(t.Context(),
		"UPDATE backup_runs SET started_at=$1 WHERE run_uid=$2", time.Now().Add(-time.Hour-time.Minute), id); err != nil {
		t.Fatal(err)
	}
	due, err = worker.due(t.Context(), recorder)
	if err != nil || !due {
		t.Fatal("abandoned run prevented the backoff-spaced retry", due, err)
	}
}

// TestFailureBackoffSchedule pins the B1 pacing table exactly: 1h, 2h, 4h,
// 8h, 16h, then the 24h cap, with defensive floors for a missing streak.
func TestFailureBackoffSchedule(t *testing.T) {
	for _, tc := range []struct {
		streak int
		want   time.Duration
	}{
		{0, time.Hour},
		{-3, time.Hour},
		{1, time.Hour},
		{2, 2 * time.Hour},
		{3, 4 * time.Hour},
		{4, 8 * time.Hour},
		{5, 16 * time.Hour},
		{6, 24 * time.Hour},
		{7, 24 * time.Hour},
		{100, 24 * time.Hour},
	} {
		if got := failureBackoff(tc.streak); got != tc.want {
			t.Fatalf("failureBackoff(%d) = %v, want %v", tc.streak, got, tc.want)
		}
	}
}

// TestDuePacesRetriesWithFailureBackoff proves the scheduler reads its pace
// from the run history: consecutive failures stretch the retry spacing, and
// one success anywhere in the chain resets it to the plain interval.
func TestDuePacesRetriesWithFailureBackoff(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	seed := func(state string, startedAgo time.Duration) {
		t.Helper()
		_, err := platform.Exec(ctx,
			"INSERT INTO backup_runs(run_uid,state,scope,started_at,finished_at) VALUES($1,$2,$3,$4,statement_timestamp())",
			fmt.Sprintf("due-%s-%d", state, startedAgo), state, ScopeComplete, now.Add(-startedAgo))
		if err != nil {
			t.Fatal(err)
		}
	}
	recorder := &StateRecorder{Pool: platform}
	worker := &Worker{Pool: platform, Interval: 24 * time.Hour, Now: func() time.Time { return now }}

	// One failure 30 minutes ago: the 1h backoff suppresses the retry.
	seed(StateFailed, 30*time.Minute)
	due, err := worker.due(ctx, recorder)
	if err != nil || due {
		t.Fatal("one failure 30m ago must be backoff-suppressed", due, err)
	}

	// Three consecutive failures, newest 2 hours ago: the 4h backoff still
	// suppresses; 4 hours plus a minute releases it.
	if _, err := platform.Exec(ctx, "DELETE FROM backup_runs"); err != nil {
		t.Fatal(err)
	}
	seed(StateFailed, 10*time.Hour)
	seed(StateFailed, 6*time.Hour)
	seed(StateFailed, 2*time.Hour)
	due, err = worker.due(ctx, recorder)
	if err != nil || due {
		t.Fatal("streak 3 at 2h must still be suppressed (4h backoff)", due, err)
	}
	_, err = platform.Exec(ctx, "UPDATE backup_runs SET started_at=$1 WHERE state=$2 AND started_at=$3",
		now.Add(-4*time.Hour-time.Minute), StateFailed, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	due, err = worker.due(ctx, recorder)
	if err != nil || !due {
		t.Fatal("streak 3 past 4h must be due", due, err)
	}

	// A success resets the streak: the newest row is a success one hour old,
	// so the 24h interval (not the backoff) suppresses the retry.
	if _, err := platform.Exec(ctx, "DELETE FROM backup_runs"); err != nil {
		t.Fatal(err)
	}
	seed(StateSuccess, time.Hour)
	streak, err := recorder.FailureStreak(ctx)
	if err != nil || streak != 0 {
		t.Fatal("success must reset the streak", streak, err)
	}
	due, err = worker.due(ctx, recorder)
	if err != nil || due {
		t.Fatal("fresh success must suppress via the interval", due, err)
	}
	_, err = platform.Exec(ctx, "UPDATE backup_runs SET started_at=$1 WHERE state=$2", now.Add(-25*time.Hour), StateSuccess)
	if err != nil {
		t.Fatal(err)
	}
	due, err = worker.due(ctx, recorder)
	if err != nil || !due {
		t.Fatal("success older than the interval must be due", due, err)
	}
}

// TestOffsiteOutageRunsAtMostSixTimesIn24h is the B1 acceptance: with the
// offsite destination failing, the hourly tick executes at most ~5 full
// local backups over 24h (t0 plus the 1h/2h/4h/8h backoff retries).
func TestOffsiteOutageRunsAtMostSixTimesIn24h(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	current := time.Now().UTC().Truncate(time.Second)
	worker := &Worker{
		Pool:       platform,
		Runner:     fakeRunner{Dir: t.TempDir()},
		Store:      failingStore{},
		Recipient:  identity.Recipient().String(),
		OffsiteURI: "s3://test-bucket/backups",
		Now:        func() time.Time { return current },
	}
	if err := worker.RunOnce(ctx); err == nil {
		t.Fatal("initial outage run must fail")
	}
	for hour := 1; hour <= 24; hour++ {
		current = current.Add(time.Hour)
		_ = worker.RunOnce(ctx) // error means a run executed and failed; nil means backoff-suppressed
	}
	var runs int
	if err := platform.Pool.QueryRow(ctx, "SELECT count(*) FROM backup_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs > 6 {
		t.Fatalf("24h outage executed %d full runs; backoff not enforced", runs)
	}
	if runs != 5 {
		t.Fatalf("expected t0 plus 1h/2h/4h/8h retries (5 runs), got %d", runs)
	}
}

// tamperingDownloadStore returns one byte more than it stored, so the
// post-upload verify pass observes a ciphertext checksum mismatch.
type tamperingDownloadStore struct{ *memoryStore }

func (s tamperingDownloadStore) Download(ctx context.Context, name string, w io.Writer) error {
	if err := s.memoryStore.Download(ctx, name, w); err != nil {
		return err
	}
	_, err := w.Write([]byte("tampered"))
	return err
}

// TestOffsiteVerifyMismatchFailsRun is B4: corruption between upload and
// verify is a failed run, never a success -- the offsite copy is not whole.
func TestOffsiteVerifyMismatchFailsRun(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	worker := &Worker{
		Pool:       platform,
		Runner:     fakeRunner{Dir: t.TempDir()},
		Store:      tamperingDownloadStore{newMemoryStore()},
		Recipient:  identity.Recipient().String(),
		OffsiteURI: "s3://test-bucket/backups",
		Now:        time.Now,
	}
	if err := worker.RunOnce(ctx); err == nil {
		t.Fatal("verify mismatch must fail the run")
	}
	var state, note string
	if err := platform.Pool.QueryRow(ctx,
		"SELECT state,error FROM backup_runs ORDER BY id DESC LIMIT 1").Scan(&state, &note); err != nil {
		t.Fatal(err)
	}
	if state != StateFailed {
		t.Fatalf("verify mismatch must record a failed run, got %q", state)
	}
	if !strings.Contains(note, "verify") || !strings.Contains(note, "checksum mismatch") {
		t.Fatalf("run error must name the verify mismatch, got %q", note)
	}
}
