package main

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/config"
)

// startBackgroundWorker is the common startup boundary for workers that may
// submit external work or consume durable side-effect leases. API handlers,
// SMTP policy/milters and configuration convergence do not pass this boundary.
func startBackgroundWorker(ctx context.Context, cfg *config.Config, run func(context.Context)) bool {
	if !cfg.BackgroundWorkersAllowed() {
		return false
	}
	go run(ctx)
	return true
}

func startPeriodicWorker(ctx context.Context, cfg *config.Config, interval time.Duration, warning string, once func(context.Context) error, logger *zap.Logger) bool {
	return startBackgroundWorker(ctx, cfg, func(ctx context.Context) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := once(ctx); err != nil && ctx.Err() == nil {
				logger.Warn(warning, zap.Error(err))
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}
