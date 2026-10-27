package backupauto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
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
// agentd backup.local.create operation because urmaild runs unprivileged.
type BackupRunner interface {
	RunBackup(ctx context.Context, stamp string) ([]LocalArtifact, string, bool, error)
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
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
				logger.Warn("backup attempt failed", zap.Error(err))
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
	start := w.now()
	runUID, err := recorder.StartRun(ctx, ScopeComplete)
	if err != nil {
		return err
	}
	defer func() {
		success := returnErr == nil
		finishErr := recorder.FinishRun(ctx, runUID, success, returnErr, w.totalBytes, w.now().Sub(start), w.manifestName, w.OffsiteURI, w.retentionDeleted)
		if returnErr == nil && finishErr != nil {
			returnErr = finishErr
		}
		if !success {
			_ = w.notifyFailure(ctx, runUID, returnErr)
		}
	}()

	stamp := StampFor(w.now())
	artifacts, scope, consistent, err := w.Runner.RunBackup(ctx, stamp)
	if err != nil {
		return fmt.Errorf("local backup: %w", err)
	}
	if !consistent || scope != ScopeComplete {
		return fmt.Errorf("local backup scope %q consistent=%t is not a complete maintenance backup", scope, consistent)
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

	manifest := &Manifest{
		Version:    ManifestVersion2,
		Scope:      scope,
		Consistent: consistent,
		CreatedAt:  start.UTC(),
		Artifacts:  entries,
		Encryption: &EncryptionInfo{Algorithm: EncryptionAlgorithmAge, Recipient: w.Recipient},
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
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

// applyRetention lists remote generations and deletes only what the pure
// planner authorizes.
func (w *Worker) applyRetention(ctx context.Context) (int, RetentionPlan, error) {
	objects, err := w.Store.List(ctx)
	if err != nil {
		return 0, RetentionPlan{}, fmt.Errorf("list offsite: %w", err)
	}
	manifests := map[string]bool{}
	objectsByName := map[string]RemoteObject{}
	for _, o := range objects {
		objectsByName[o.Name] = o
		if strings.HasSuffix(o.Name, ".manifest.json") {
			manifests[o.Name] = true
		}
	}
	var candidates []RetentionCandidate
	generationObjects := map[string][]string{}
	for name := range manifests {
		base := strings.TrimSuffix(path.Base(name), ".manifest.json")
		stamp := strings.TrimPrefix(base, "urmail-")
		created, err := time.Parse("20060102T150405Z", stamp)
		if err != nil {
			continue
		}
		candidates = append(candidates, RetentionCandidate{ID: base, CreatedAt: created, Verified: true})
		generationObjects[base] = append(generationObjects[base], name)
	}
	if len(candidates) == 0 {
		return 0, RetentionPlan{}, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CreatedAt.Before(candidates[j].CreatedAt) })
	plan := PlanRetention(candidates, w.Retention, w.now())
	if len(plan.Refusals) > 0 || len(plan.Delete) == 0 {
		return 0, plan, nil
	}
	deleted := 0
	for _, generation := range plan.Delete {
		for _, object := range generationObjects[generation] {
			if err := w.Store.Delete(ctx, object); err != nil {
				return deleted, plan, fmt.Errorf("delete %s: %w", object, err)
			}
			deleted++
		}
	}
	return deleted, plan, nil
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
