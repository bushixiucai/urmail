package backupauto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	return w.Pool == nil || w.Runner == nil || w.Store == nil || w.Recipient == "" || w.OffsiteURI == ""
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
		return true, nil
	}
	interval := w.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	return w.now().Sub(last.StartedAt) >= interval, nil
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
	start := w.now()
	runUID, startErr := recorder.StartRun(ctx, ScopeComplete)
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
		if !success {
			_ = w.notifyFailure(ctx, runUID, returnErr)
		}
	}()

	stamp := StampFor(w.now())
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
	entries := make([]Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.SHA256 == "" || artifact.Size < 0 {
			return fmt.Errorf("artifact %s lacks plaintext hash recorded by the runner", artifact.Name)
		}
		entry, err := EncryptAndUpload(ctx, w.Store, artifact, w.Recipient)
		if err != nil {
			return err
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
		CreatedAt:     start.UTC(),
		Artifacts:     entries,
		Encryption:    &EncryptionInfo{Algorithm: EncryptionAlgorithmAge, Recipient: w.Recipient},
		Slots:         slots,
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if _, err = retentionManifest(raw, stamp, w.now()); err != nil {
		return fmt.Errorf("new backup manifest is incomplete: %w", err)
	}
	w.manifestName = ManifestName(stamp)
	w.manifestRaw = string(raw) + "\n"
	if err := w.Store.Upload(ctx, w.manifestName, strings.NewReader(w.manifestRaw), int64(len(w.manifestRaw))); err != nil {
		return fmt.Errorf("upload manifest: %w", err)
	}

	// Verify BEFORE recording success: download every object of THIS run
	// and re-checksum the ciphertext. The worker never holds the age private
	// key, so plaintext verification is the operator verify-offsite duty.
	if err := w.verifyCurrentRun(ctx, manifest); err != nil {
		return err
	}
	if err := recorder.MarkVerifying(ctx, runUID); err != nil {
		return err
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

// verifyCurrentRun re-downloads and checksums the objects this run uploaded.
func (w *Worker) verifyCurrentRun(ctx context.Context, manifest *Manifest) error {
	for _, artifact := range manifest.Artifacts {
		remote, err := checksumRemote(ctx, w.Store, artifact.EncryptedName)
		if err != nil {
			return fmt.Errorf("verify %s: %w", artifact.EncryptedName, err)
		}
		if remote != artifact.EncryptedSHA256 {
			return fmt.Errorf("verify %s: ciphertext checksum mismatch (remote %s manifest %s)", artifact.EncryptedName, remote, artifact.EncryptedSHA256)
		}
	}
	remoteManifest, err := checksumRemote(ctx, w.Store, w.manifestName)
	if err != nil {
		return fmt.Errorf("verify manifest: %w", err)
	}
	if remoteManifest != sha256Hex([]byte(w.manifestRaw)) {
		return fmt.Errorf("verify manifest: checksum mismatch for %s", w.manifestName)
	}
	return nil
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

// sha256Hex returns the hex SHA-256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
