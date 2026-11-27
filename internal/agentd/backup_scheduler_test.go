package agentd

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
)

func TestBackupSchedulerReadinessUsesHealthFileWithoutSystemd(t *testing.T) {
	stateRoot := t.TempDir()
	now := time.Now()

	manager := &ApplyManager{
		cfg:  &config.Config{},
		root: stateRoot,
		run: func(context.Context, string, ...string) error {
			t.Fatal("probeBackupScheduler must not invoke systemctl or external commands")
			return nil
		},
	}

	// Missing health file must fail closed.
	if err := manager.probeBackupScheduler(t.Context()); err == nil {
		t.Fatal("expected probeBackupScheduler to fail when health file is missing")
	}

	// Live scheduler writes health file; probe passes without systemd.
	var runs atomic.Int32
	sched := &BackupScheduler{
		StateRoot: stateRoot,
		Interval:  20 * time.Millisecond,
		Clock:     time.Now,
		RunBackup: func(context.Context) error {
			runs.Add(1)
			return nil
		},
	}
	stop := sched.Start(t.Context())
	defer stop()

	if err := manager.probeBackupScheduler(t.Context()); err != nil {
		t.Fatalf("expected live scheduler probe to pass without systemd, got: %v", err)
	}

	// Trigger manual run and wait for execution.
	sched.Trigger()
	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runs.Load() < 1 {
		t.Fatal("expected BackupScheduler to execute triggered backup pass")
	}

	// Stale health file must fail closed.
	stop()
	staleSched := &BackupScheduler{
		StateRoot: stateRoot,
		Clock:     func() time.Time { return now.Add(-5 * time.Hour) },
		RunBackup: func(context.Context) error { return nil },
	}
	if err := staleSched.RunOnce(t.Context(), "timer"); err != nil {
		t.Fatal(err)
	}
	if err := manager.probeBackupScheduler(t.Context()); err == nil {
		t.Fatal("expected stale scheduler health file to fail probe")
	}

	// Held/dev/quarantined modes still skip scheduler probe.
	for _, cfg := range []*config.Config{nil, {WorkersHeld: true}, {DevMode: true}, {Quarantined: true}} {
		heldMgr := &ApplyManager{cfg: cfg, root: t.TempDir()}
		if err := heldMgr.probeBackupScheduler(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
