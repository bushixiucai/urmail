package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
)

// BackupSchedulerHealthFile is the agent-state health record written by the
// internal backup scheduler so readiness probes do not depend on systemd.
const BackupSchedulerHealthFile = "backup-scheduler.json"

// DefaultBackupSchedulerInterval matches the legacy urmail-backup-auto.timer cadence.
const DefaultBackupSchedulerInterval = time.Hour

// BackupSchedulerHealth describes the live state of the in-process backup scheduler.
type BackupSchedulerHealth struct {
	Status      string    `json:"status"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastRunAt   time.Time `json:"last_run_at,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	IntervalSec int64     `json:"interval_sec"`
	Trigger     string    `json:"trigger,omitempty"`
}

// BackupScheduler runs periodic and manually triggered complete backups
// driven by configured backup schedules without relying on systemd timers.
type BackupScheduler struct {
	StateRoot    string
	Pool         *db.Pool
	Interval     time.Duration
	Clock        func() time.Time
	RunBackup    func(context.Context) error
	ReadInterval func(context.Context) (time.Duration, error)
	Logger       *slog.Logger

	triggerCh chan struct{}
	mu        sync.Mutex
	health    BackupSchedulerHealth
}

var activeBackupScheduler atomic.Pointer[BackupScheduler]

// RegisterBackupScheduler binds the process-wide scheduler used by backup.local.create.
func RegisterBackupScheduler(s *BackupScheduler) {
	activeBackupScheduler.Store(s)
}

// StateRoot returns the manager's resolved state root directory.
func (m *ApplyManager) StateRoot() string {
	return m.root
}

func (s *BackupScheduler) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *BackupScheduler) interval(ctx context.Context) time.Duration {
	if s.ReadInterval != nil {
		if d, err := s.ReadInterval(ctx); err == nil && d > 0 {
			return d
		}
	}
	if s.Interval > 0 {
		return s.Interval
	}
	return DefaultBackupSchedulerInterval
}

// Trigger requests an immediate non-blocking backup pass on the running scheduler.
func (s *BackupScheduler) Trigger() bool {
	if s == nil || s.triggerCh == nil {
		return false
	}
	select {
	case s.triggerCh <- struct{}{}:
		return true
	default:
		return true
	}
}

// Start launches the periodic backup loop and returns a stop function that
// waits for the loop to exit cleanly.
func (s *BackupScheduler) Start(ctx context.Context) func() {
	if s.triggerCh == nil {
		s.triggerCh = make(chan struct{}, 1)
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	interval := s.interval(loopCtx)
	s.mu.Lock()
	s.health = BackupSchedulerHealth{
		Status:      "running",
		UpdatedAt:   s.now().UTC(),
		IntervalSec: int64(interval.Seconds()),
	}
	_ = s.writeHealthLocked()
	s.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				s.mu.Lock()
				s.health.Status = "stopped"
				s.health.UpdatedAt = s.now().UTC()
				_ = s.writeHealthLocked()
				s.mu.Unlock()
				return
			case <-ticker.C:
				_ = s.RunOnce(loopCtx, "timer")
			case <-s.triggerCh:
				_ = s.RunOnce(loopCtx, "manual")
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// RunOnce executes one backup pass, updates the scheduler health file, and
// returns any execution error.
func (s *BackupScheduler) RunOnce(ctx context.Context, trigger string) error {
	runFn := s.RunBackup
	if runFn == nil {
		runFn = defaultRunBackupAuto
	}
	interval := s.interval(ctx)
	runErr := runFn(ctx)

	s.mu.Lock()
	s.health.Status = "running"
	s.health.UpdatedAt = s.now().UTC()
	s.health.LastRunAt = s.health.UpdatedAt
	s.health.IntervalSec = int64(interval.Seconds())
	s.health.Trigger = trigger
	if runErr != nil {
		s.health.LastError = runErr.Error()
	} else {
		s.health.LastError = ""
	}
	writeErr := s.writeHealthLocked()
	s.mu.Unlock()

	if runErr != nil {
		if s.Logger != nil {
			s.Logger.Warn("backup scheduler pass failed", "trigger", trigger, "error", runErr)
		}
		return runErr
	}
	return writeErr
}

func (s *BackupScheduler) writeHealthLocked() error {
	if s.StateRoot == "" {
		return nil
	}
	if err := os.MkdirAll(s.StateRoot, 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(s.health)
	if err != nil {
		return err
	}
	_, err = writeFileAtomic(filepath.Join(s.StateRoot, BackupSchedulerHealthFile), encoded, 0o600, []string{s.StateRoot})
	return err
}

func defaultRunBackupAuto(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/urmailctl", "backup-auto") // #nosec G204 -- fixed binary and subcommand; no caller input.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("urmailctl backup-auto: %w: %s", err, string(out))
	}
	return nil
}

func readBackupSchedulerHealth(stateRoot string) (BackupSchedulerHealth, error) {
	if stateRoot == "" {
		return BackupSchedulerHealth{}, errors.New("backup scheduler state root is empty")
	}
	raw, err := os.ReadFile(filepath.Join(stateRoot, BackupSchedulerHealthFile)) // #nosec G304 -- fixed state-root health file path.
	if err != nil {
		return BackupSchedulerHealth{}, err
	}
	var h BackupSchedulerHealth
	if err := json.Unmarshal(raw, &h); err != nil {
		return BackupSchedulerHealth{}, fmt.Errorf("decode backup scheduler health: %w", err)
	}
	return h, nil
}
