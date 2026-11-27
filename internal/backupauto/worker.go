package backupauto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
)

// DefaultInterval is the backup cadence when none is configured.
const DefaultInterval = 24 * time.Hour

// staleRunThreshold is how long a running row may persist before a restart
// marks it failed. It only rewrites database state; artifacts are untouched.
const staleRunThreshold = 24 * time.Hour

// BackupRunner executes the privileged local backup (pg dump + tars) and
// returns the plaintext artifacts it produced. Production wires this to the
// independent root CLI service so stopping mail writers never stops the job.
type BackupRunner interface {
	RunBackup(ctx context.Context, stamp string) ([]LocalArtifact, string, bool, error)
}

// ManifestRunner is an optional extension of BackupRunner that provides the full
// manifest (including Version 3 layout and logical slots) alongside artifacts.
type ManifestRunner interface {
	RunBackupManifest(ctx context.Context, stamp string) (*Manifest, []LocalArtifact, error)
}

// Worker runs backup -> encrypt -> upload -> verify -> retention on an
// hourly tick (same cadence as the billing renewal worker), executing a
// full pipeline at most once per Interval. Every failure is recorded in
// backup_runs and surfaced in the notification outbox; no failure path ever
// deletes or overwrites an existing offsite object.
type Worker struct {
	Pool     *db.Pool
	Runner   BackupRunner
	Store    OffsiteStore
	Recorder *StateRecorder

	// Recipient is the age public key artifacts are encrypted to. The
	// matching private key is NEVER stored on this host.
	Recipient string
	// OffsiteURI is the s3:// or sftp:// destination.
	OffsiteURI string
	// LocalOnly is an explicit operator approval to record a local complete
	// backup when no offsite destination is configured. The run is marked
	// pending_local_only; it is never reported as uploaded.
	LocalOnly bool
	// Interval is the minimum spacing between successful full runs.
	Interval time.Duration
	// Retention policy applied to remote generations after a verified run.
	Retention RetentionPolicy
	// RetentionMaxVerifyBytes bounds streaming verification across all generations.
	// Zero defaults to 1 TiB; refusal never authorizes partial pruning.
	RetentionMaxVerifyBytes int64
	// RetentionVerifyTimeout bounds offsite retention I/O; zero defaults to 1h.
	RetentionVerifyTimeout time.Duration

	// Now allows tests to pin the clock.
	Now func() time.Time

	// Per-run bookkeeping (single worker goroutine; no locking needed).
	totalBytes       int64
	manifestName     string
	manifestRaw      string
	retentionDeleted int
	retentionNote    string
	// offsiteFailed marks runs that already raised the offsite-specific
	// backup_offsite_failed alert so the generic backup_failed row is not
	// double-sent for the same failure (B3).
	offsiteFailed bool
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Run blocks until ctx is done, ticking hourly.
func (w *Worker) Run(ctx context.Context, logger *zap.Logger) {
	if w.disabled() {
		logger.Info("automated offsite backup disabled (no recipient/destination configured)")
		return
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
		logger.Warn("backup attempt failed", zap.Error(err))
	} else if w.retentionNote != "" {
		logger.Warn("backup retention refused", zap.String("reason", w.retentionNote))
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
				logger.Warn("backup attempt failed", zap.Error(err))
			} else if w.retentionNote != "" {
				logger.Warn("backup retention refused", zap.String("reason", w.retentionNote))
			}
		}
	}
}

func (w *Worker) disabled() bool {
	if w.Pool == nil || w.Runner == nil {
		return true
	}
	if w.LocalOnly {
		return w.Store != nil || w.Recipient != "" || w.OffsiteURI != ""
	}
	return w.Store == nil || w.Recipient == "" || w.OffsiteURI == ""
}

// RunOnce executes one due check and, when due, the full pipeline.
func (w *Worker) RunOnce(ctx context.Context) error {
	if w.disabled() {
		return nil
	}
	recorder := w.Recorder
	if recorder == nil {
		recorder = &StateRecorder{Pool: w.Pool}
	}
	// Resolve crashed runs first so they cannot suppress retries.
	if _, err := recorder.StaleRunning(ctx, staleRunThreshold, w.now()); err != nil {
		return fmt.Errorf("resolve stale backup runs: %w", err)
	}
	due, err := w.due(ctx, recorder)
	if err != nil {
		return err
	}
	if !due {
		return nil
	}
	return w.runPipeline(ctx, recorder)
}

func (w *Worker) due(ctx context.Context, recorder *StateRecorder) (bool, error) {
	last, err := recorder.LastRun(ctx)
	if errors.Is(err, ErrNoRuns) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if last.State == StateRunning || last.State == StateVerifying {
		// A fresh crash is not yet stale; do not double-run on top of it.
		return false, nil
	}
	if last.State == StateFailed {
		// B1 (v0.6.25): a failing pipeline retries on an exponential backoff
		// derived from the consecutive failure streak, not on the next tick.
		// The streak lives in backup_runs, so the pacing survives restarts,
		// and one success anywhere in the chain resets it.
		streak, err := recorder.FailureStreak(ctx)
		if err != nil {
			return false, err
		}
		return w.now().Sub(last.StartedAt) >= failureBackoff(streak), nil
	}
	interval := w.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	return w.now().Sub(last.StartedAt) >= interval, nil
}

// failureBackoff spaces retries of a failing backup pipeline: 1h, 2h, 4h,
// 8h, 16h, then a 24h cap. Without it the hourly tick turns a degraded
// offsite destination into an hourly full local backup with no spool
// pruning (B1): over 24h the schedule below allows at most ~5 runs.
func failureBackoff(consecutiveFailures int) time.Duration {
	if consecutiveFailures < 1 {
		consecutiveFailures = 1
	}
	if consecutiveFailures >= 6 {
		return 24 * time.Hour
	}
	return time.Duration(1<<(consecutiveFailures-1)) * time.Hour
}

// runPipeline is the strict backup -> encrypt -> upload -> verify ->
// retention order. The offsite copy is verified BEFORE the run is recorded
// as success and BEFORE any retention decision, so a failed upload can
// never authorize deletion of older generations.
func (w *Worker) runPipeline(ctx context.Context, recorder *StateRecorder) (returnErr error) {
	w.totalBytes = 0
	w.manifestName = ""
	w.manifestRaw = ""
	w.retentionDeleted = 0
	w.retentionNote = ""
	w.offsiteFailed = false
	start := w.now()
	runUID, startErr := recorder.StartRun(ctx, ScopeComplete, start)
	if startErr != nil {
		return startErr
	}
	defer func() {
		success := returnErr == nil
		finishReason := returnErr
		if success && w.retentionNote != "" {
			finishReason = errors.New(w.retentionNote)
		}
		finishErr := recorder.FinishRun(ctx, runUID, success, finishReason, w.totalBytes, w.now().Sub(start), w.manifestName, w.OffsiteURI, w.retentionDeleted)
		if returnErr == nil && finishErr != nil {
			returnErr = finishErr
		}
		if !success && !w.offsiteFailed {
			// B3: an offsite failure already raised the offsite-specific
			// alert; every other failure gets the generic one exactly once.
			_ = w.notifyFailure(ctx, runUID, returnErr)
		}
	}()

	backupTime := w.now().UTC()
	stamp := StampFor(backupTime)
	var (
		sourceManifest *Manifest
		artifacts      []LocalArtifact
		scope          string
		consistent     bool
		err            error
	)
	if mr, ok := w.Runner.(ManifestRunner); ok {
		sourceManifest, artifacts, err = mr.RunBackupManifest(ctx, stamp)
		if err == nil && sourceManifest != nil {
			scope = sourceManifest.Scope
			consistent = sourceManifest.Consistent
		}
	} else {
		artifacts, scope, consistent, err = w.Runner.RunBackup(ctx, stamp)
	}
	if err != nil {
		return fmt.Errorf("local backup: %w", err)
	}
	if !consistent || scope != ScopeComplete {
		return fmt.Errorf("local backup scope %q consistent=%t is not a complete maintenance backup", scope, consistent)
	}

	if err = completeLocalArtifacts(stamp, artifacts); err != nil {
		return err
	}
	if w.LocalOnly {
		// The local generation's own manifest names the generation for the
		// freshness gate; no offsite object exists.
		w.manifestName = ManifestName(stamp)
		if err := recorder.SetOffsiteState(ctx, runUID, OffsiteStatePendingLocalOnly); err != nil {
			return err
		}
	} else if _, offsiteErr := w.runOffsitePhase(ctx, stamp, backupTime, artifacts, sourceManifest, scope, consistent); offsiteErr != nil {
		// v0.6.25 semantics: an offsite destination is configured, so a run
		// whose offsite copy failed can never satisfy the "fresh, fully
		// verified backup" release gate. The local generation stays on disk
		// untouched and retention stays refused fail-closed (this return
		// happens before applyRetention), but the run row must record the
		// failure -- never a success with a mere "offsite skipped" note.
		// B1: the kept generation's disk cost rides along in the error text
		// because failed generations are not pruned.
		w.offsiteFailed = true
		_ = w.notifyOffsiteFailure(ctx, runUID, offsiteErr)
		return fmt.Errorf("offsite copy failed (local generation kept: %s; retention skipped): %w",
			localGenerationUsage(artifacts), offsiteErr)
	}

	if !w.LocalOnly {
		if err := recorder.SetOffsiteState(ctx, runUID, OffsiteStateUploaded); err != nil {
			return err
		}
	}
	if err := recorder.MarkVerifying(ctx, runUID); err != nil {
		return err
	}

	if w.LocalOnly {
		// No offsite inventory exists to prune; local generations are kept
		// and the run still counts as a verified local complete backup.
		w.retentionNote = "retention skipped: local-only backup has no offsite inventory"
		return nil
	}
	// Retention runs last and refuses whenever the planner is not certain.
	deleted, plan, err := w.applyRetention(ctx)
	w.retentionDeleted = deleted
	switch {
	case err != nil:
		// Retention failure must not fail the backup: the new generation is
		// verified and stored. Record the refusal in the run row via error-
		// free return and surface it in logs.
		w.retentionNote = fmt.Sprintf("retention skipped: %v", err)
	case len(plan.Refusals) > 0:
		w.retentionNote = "retention refused: " + strings.Join(plan.Refusals, "; ")
	default:
		w.retentionDeleted = deleted
	}
	return nil
}

// runOffsitePhase encrypts and uploads every local artifact, writes the
// run manifest and verifies the new generation offsite before the caller
// may record success or authorize retention. The returned manifest is nil
// whenever the phase fails; local artifacts are untouched.
func (w *Worker) runOffsitePhase(ctx context.Context, stamp string, backupTime time.Time, artifacts []LocalArtifact, sourceManifest *Manifest, scope string, consistent bool) (*Manifest, error) {
	entries := make([]Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.SHA256 == "" || artifact.Size < 0 {
			return nil, fmt.Errorf("artifact %s lacks plaintext hash recorded by the runner", artifact.Name)
		}
		entry, err := EncryptAndUpload(ctx, w.Store, artifact, w.Recipient)
		if err != nil {
			return nil, err
		}
		entry.Mode = 0o600
		entries = append(entries, entry)
		w.totalBytes += entry.EncryptedSize
	}

	manifestVersion := ManifestVersion2
	layoutVersion := 0
	var slots map[string]Slot
	if sourceManifest != nil && sourceManifest.Version == ManifestVersion3 {
		manifestVersion = ManifestVersion3
		layoutVersion = sourceManifest.LayoutVersion
		slots = sourceManifest.Slots
	}

	manifest := &Manifest{
		Version:       manifestVersion,
		LayoutVersion: layoutVersion,
		Scope:         scope,
		Consistent:    consistent,
		CreatedAt:     backupTime,
		Artifacts:     entries,
		Encryption:    &EncryptionInfo{Algorithm: EncryptionAlgorithmAge, Recipient: w.Recipient},
		Slots:         slots,
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if _, err = retentionManifest(raw, stamp, w.now()); err != nil {
		return nil, fmt.Errorf("new backup manifest is incomplete: %w", err)
	}
	w.manifestName = ManifestName(stamp)
	w.manifestRaw = string(raw) + "\n"
	if err := w.Store.Upload(ctx, w.manifestName, strings.NewReader(w.manifestRaw), int64(len(w.manifestRaw))); err != nil {
		return nil, fmt.Errorf("upload manifest: %w", err)
	}

	// Verify BEFORE recording success: download every object of THIS run
	// and re-checksum the ciphertext. The worker never holds the age private
	// key, so plaintext verification is the operator verify-offsite duty.
	if err := w.verifyCurrentRun(ctx, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// verifyCurrentRun re-downloads and checksums the objects this run uploaded.
func (w *Worker) verifyCurrentRun(ctx context.Context, manifest *Manifest) error {
	return VerifyUploadedGeneration(ctx, w.Store, manifest, w.manifestName, w.manifestRaw)
}

// notifyFailure inserts one platform-scoped outbox row so failed runs are
// observable in-app (same delivery shape as platform observability alerts).
func (w *Worker) notifyFailure(ctx context.Context, runUID string, _ error) error {
	params, _ := json.Marshal(map[string]string{
		"kind":   "backup_failed",
		"source": "backup:" + runUID,
		"state":  "firing",
	})
	_, err := w.Pool.Exec(ctx,
		`INSERT INTO notification_outbox(kind,message_key,params,dedupe_key,channel,state)
		 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(dedupe_key) DO NOTHING`,
		"backup_failed", "notification.operational_firing", params,
		"backup:"+runUID+":failed", "in_app", "delivered")
	return err
}

// notifyOffsiteFailure inserts one platform-scoped outbox row when the
// offsite copy of a run fails: the run row is recorded failed (v0.6.25
// semantics), and this alert gives the operator the offsite-specific cause
// in addition to the generic backup_failed notice raised by runPipeline.
func (w *Worker) notifyOffsiteFailure(ctx context.Context, runUID string, _ error) error {
	params, _ := json.Marshal(map[string]string{
		"kind":   "backup_offsite_failed",
		"source": "backup:" + runUID,
		"state":  "firing",
	})
	_, err := w.Pool.Exec(ctx,
		`INSERT INTO notification_outbox(kind,message_key,params,dedupe_key,channel,state)
		 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(dedupe_key) DO NOTHING`,
		"backup_offsite_failed", "notification.operational_firing", params,
		"backup:"+runUID+":offsite-failed", "in_app", "delivered")
	return err
}

// sha256Hex returns the hex SHA-256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// localGenerationUsage summarizes the disk cost of one kept-but-unshipped
// local generation (B1): a failed offsite phase leaves its artifacts in the
// private spool with no pruning, so the run error text reports what each
// failure accumulates and where.
func localGenerationUsage(artifacts []LocalArtifact) string {
	var total int64
	dir := ""
	for _, artifact := range artifacts {
		total += artifact.Size
		if dir == "" {
			dir = filepath.Dir(artifact.Path)
		}
	}
	return fmt.Sprintf("%d artifacts, %.1f MiB in %s", len(artifacts), float64(total)/(1<<20), dir)
}
