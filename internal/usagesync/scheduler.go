// Package usagesync measures and synchronizes mailbox storage usage without systemd.
package usagesync

import (
	"context"
	"log/slog"
	"time"
)

// DefaultUsageSyncInterval matches the legacy urmail-usage-sync.timer cadence.
const DefaultUsageSyncInterval = 5 * time.Minute

// Scheduler runs periodic mailbox storage usage synchronization without systemd.
type Scheduler struct {
	Interval time.Duration
	Clock    func() time.Time
	SyncFn   func(context.Context) (int, error)
	Logger   *slog.Logger
}

func (s *Scheduler) interval() time.Duration {
	if s.Interval > 0 {
		return s.Interval
	}
	return DefaultUsageSyncInterval
}

// Start launches the periodic usage sync loop and returns a stop function.
func (s *Scheduler) Start(ctx context.Context) func() {
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	interval := s.interval()

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				_, _ = s.RunOnce(loopCtx)
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// RunOnce executes one usage sync pass.
func (s *Scheduler) RunOnce(ctx context.Context) (int, error) {
	if s.SyncFn == nil {
		return 0, nil
	}
	n, err := s.SyncFn(ctx)
	if err != nil && s.Logger != nil {
		s.Logger.Warn("scheduled usage sync failed", "error", err)
	}
	return n, err
}
