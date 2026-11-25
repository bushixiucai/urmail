package backupauto

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/db"
)

// Offsite dispositions recorded in backup_runs.offsite_state (migration 0160).
// Historical rows keep ""; gates treat empty or unknown values as unsatisfied.
const (
	OffsiteStateUploaded         = "uploaded"
	OffsiteStatePendingLocalOnly = "pending_local_only"
)

// Run states recorded in backup_runs (migration 0120).
const (
	StateRunning   = "running"
	StateVerifying = "verifying"
	StateSuccess   = "success"
	StateFailed    = "failed"
)

// StateRecorder persists run state in the backup_runs table. It uses
// package-local parameterized SQL instead of the shared sqlc queries:
// internal/store is a collision hotspot for parallel work streams and this
// table has exactly three writers, all inside this package. Moving the
// queries to internal/store/queries/platform_backup.sql is follow-up work.
type StateRecorder struct {
	Pool *db.Pool
}

// newRunUID returns a random run identifier.
func newRunUID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// StartRun inserts the initial running row with the scheduler's own clock
// and returns its run uid. The caller-supplied timestamp keeps the run
// history and the due/backoff math on one clock (tests pin it).
func (s *StateRecorder) StartRun(ctx context.Context, scope string, startedAt time.Time) (string, error) {
	uid, err := newRunUID()
	if err != nil {
		return "", err
	}
	if s.Pool.IsSQLite() {
		_, err = s.Pool.Exec(ctx,
			"INSERT INTO backup_runs(run_uid,state,scope,started_at) VALUES($1,$2,$3,$4)",
			uid, StateRunning, scope, startedAt.UTC().UnixMicro())
	} else {
		_, err = s.Pool.Exec(ctx,
			"INSERT INTO backup_runs(run_uid,state,scope,started_at) VALUES($1,$2,$3,$4)",
			uid, StateRunning, scope, startedAt)
	}
	if err != nil {
		return "", fmt.Errorf("record backup run start: %w", err)
	}
	return uid, nil
}

// FailureStreak counts the consecutive failed runs ending at the newest row.
// The backoff scheduler derives its pace from it, so the pacing survives
// restarts; any success in the chain resets it to zero. The scan is bounded
// because the backoff itself caps out after six failures.
func (s *StateRecorder) FailureStreak(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx, "SELECT state FROM backup_runs ORDER BY id DESC LIMIT 32")
	if err != nil {
		return 0, fmt.Errorf("read backup failure streak: %w", err)
	}
	defer rows.Close()
	streak := 0
	for rows.Next() {
		var state string
		if err := rows.Scan(&state); err != nil {
			return 0, fmt.Errorf("read backup failure streak: %w", err)
		}
		if state != StateFailed {
			break
		}
		streak++
	}
	return streak, rows.Err()
}

// SetOffsiteState records the destination disposition of a run. Historical
// rows stay empty; gates must treat empty or invalid values as unsatisfied.
func (s *StateRecorder) SetOffsiteState(ctx context.Context, runUID, state string) error {
	_, err := s.Pool.Exec(ctx, "UPDATE backup_runs SET offsite_state=$2 WHERE run_uid=$1", runUID, state)
	if err != nil {
		return fmt.Errorf("record backup offsite state: %w", err)
	}
	return nil
}

// MarkVerifying moves a run into the verify stage.
func (s *StateRecorder) MarkVerifying(ctx context.Context, runUID string) error {
	_, err := s.Pool.Exec(ctx,
		"UPDATE backup_runs SET state=$2 WHERE run_uid=$1 AND state=$3",
		runUID, StateVerifying, StateRunning)
	return err
}

// FinishRun closes a run as success or failure with observed byte and
// duration totals.
func (s *StateRecorder) FinishRun(ctx context.Context, runUID string, success bool, backupErr error, bytes int64, duration time.Duration, manifestName, offsiteURI string, retentionDeleted int) error {
	state := StateSuccess
	errorText := ""
	if !success {
		state = StateFailed
		errorText = "backup failed"
		if backupErr != nil {
			errorText = backupErr.Error()
		}
		if len(errorText) > 2000 {
			errorText = errorText[:2000]
		}
	}
	if success && backupErr != nil {
		errorText = backupErr.Error()
		if len(errorText) > 2000 {
			errorText = errorText[:2000]
		}
	}
	if retentionDeleted < 0 {
		retentionDeleted = 0
	}
	var tag pgconn.CommandTag
	var err error
	if s.Pool.IsSQLite() {
		tag, err = s.Pool.Exec(ctx, `UPDATE backup_runs SET
			state=$2, finished_at=$9, bytes=$3,
			duration_ms=$4, error=$5, manifest_name=$6, offsite_uri=$7,
			retention_deleted=$8
			WHERE run_uid=$1`,
			runUID, state, bytes, duration.Milliseconds(), errorText, manifestName, offsiteURI, retentionDeleted, time.Now().UTC().UnixMicro())
	} else {
		tag, err = s.Pool.Exec(ctx, `UPDATE backup_runs SET
			state=$2, finished_at=statement_timestamp(), bytes=$3,
			duration_ms=$4, error=$5, manifest_name=$6, offsite_uri=$7,
			retention_deleted=$8
			WHERE run_uid=$1`,
			runUID, state, bytes, duration.Milliseconds(), errorText, manifestName, offsiteURI, retentionDeleted)
	}
	if err != nil {
		return fmt.Errorf("record backup run finish: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("record backup run finish: run %s vanished", runUID)
	}
	return nil
}

// ErrNoRuns is returned by LastRun when no backup run exists yet.
var ErrNoRuns = errors.New("no backup run recorded")

// RunRow is the summary the scheduler needs for due checks.
type RunRow struct {
	RunUID    string
	State     string
	StartedAt time.Time
}

// LastRun returns the most recent run row, or ErrNoRuns when none exists yet.
func (s *StateRecorder) LastRun(ctx context.Context) (*RunRow, error) {
	row := s.Pool.QueryRow(ctx,
		"SELECT run_uid,state,started_at FROM backup_runs ORDER BY id DESC LIMIT 1")
	var r RunRow
	err := row.Scan(&r.RunUID, &r.State, &r.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoRuns
	}
	if err != nil {
		return nil, fmt.Errorf("read last backup run: %w", err)
	}
	return &r, nil
}

// StaleRunning resolves a run left in running/verifying by a crash. The
// failed row keeps the error trail observable and prevents a stale run from
// suppressing the next attempt; no backup artifact is touched.
func (s *StateRecorder) StaleRunning(ctx context.Context, olderThan time.Duration, now time.Time) (int64, error) {
	var tag pgconn.CommandTag
	var err error
	if s.Pool.IsSQLite() {
		stamp := now.UTC().UnixMicro()
		tag, err = s.Pool.Exec(ctx, `UPDATE backup_runs SET state=$2,
			finished_at=$1,
			duration_ms=MAX(0, ($1-started_at)/1000),
			error=$3 WHERE state IN($4,$5) AND started_at < $1`,
			stamp, StateFailed, "run abandoned by restart", StateRunning, StateVerifying)
	} else {
		tag, err = s.Pool.Exec(ctx, `UPDATE backup_runs SET state=$2,
			finished_at=statement_timestamp(),
			duration_ms=GREATEST(0, (extract(epoch FROM statement_timestamp()-started_at)*1000)::bigint),
			error=$3 WHERE state IN($4,$5) AND started_at < $1`,
			now.Add(-olderThan), StateFailed, "run abandoned by restart", StateRunning, StateVerifying)
	}
	return tag.RowsAffected(), err
}

// ErrBackupOffsiteDisabled is returned by LastRunHealth when the backup_runs
// table does not exist: the schema predates migration 0120 or the feature was
// never provisioned, so there is no offsite state to probe.
var ErrBackupOffsiteDisabled = errors.New("backup_runs table is absent")

// RunHealth is the readiness view of the most recent backup run.
type RunHealth struct {
	RunUID    string
	State     string
	Scope     string
	StartedAt time.Time
	Error     string
	// ManifestName is the offsite manifest object of the run; its stamp is
	// the generation's own clock, which freshness gates must honor.
	ManifestName string
	// OffsiteState is '' for historical/unknown rows, 'uploaded' for an
	// offsite copy, or 'pending_local_only' for an explicitly approved local
	// complete backup whose offsite copy is owed after the cut.
	OffsiteState string
	// OffsiteURI must be non-empty for an uploaded run.
	OffsiteURI string
}

// StaleRun reports whether a run has been in running/verifying for longer
// than the crash threshold: the worker that owns it stopped making progress.
// A successful or failed row is never stale.
func (h *RunHealth) StaleRun(now time.Time) bool {
	if h == nil {
		return false
	}
	if h.State != StateRunning && h.State != StateVerifying {
		return false
	}
	return now.Sub(h.StartedAt) > staleRunThreshold
}

// LastRunHealth returns the readiness summary of the most recent run, or
// ErrNoRuns when the table exists but no run has been recorded yet. A missing
// backup_runs table yields ErrBackupOffsiteDisabled so callers can treat a
// pre-0120 database as "feature disabled" instead of unhealthy. Scope and
// ManifestName ride along so gates can demand a COMPLETE, fresh generation
// rather than any success row. The read uses the platform pool exactly like
// the writers; backup_runs denies tenant contexts by policy.
func (s *StateRecorder) LastRunHealth(ctx context.Context) (*RunHealth, error) {
	var h RunHealth
	var err error
	if s.Pool.IsSQLite() {
		var startedMicros int64
		err = s.Pool.QueryRow(ctx,
			"SELECT run_uid,state,scope,started_at,error,manifest_name,offsite_state,offsite_uri FROM backup_runs ORDER BY id DESC LIMIT 1").
			Scan(&h.RunUID, &h.State, &h.Scope, &startedMicros, &h.Error, &h.ManifestName, &h.OffsiteState, &h.OffsiteURI)
		if err == nil {
			h.StartedAt = time.UnixMicro(startedMicros).UTC()
			return &h, nil
		}
	} else {
		err = s.Pool.QueryRow(ctx,
			"SELECT run_uid,state,scope,started_at,error,manifest_name,offsite_state,offsite_uri FROM backup_runs ORDER BY id DESC LIMIT 1").
			Scan(&h.RunUID, &h.State, &h.Scope, &h.StartedAt, &h.Error, &h.ManifestName, &h.OffsiteState, &h.OffsiteURI)
		if err == nil {
			return &h, nil
		}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
		return nil, ErrBackupOffsiteDisabled
	}
	if s.Pool.IsSQLite() && strings.Contains(err.Error(), "no such table: backup_runs") {
		return nil, ErrBackupOffsiteDisabled
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoRuns
	}
	return nil, fmt.Errorf("read backup run health: %w", err)
}
