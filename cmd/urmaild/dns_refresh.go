package main

import (
	"context"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
)

type dnsReconcileTrigger interface{ Trigger() }

func startDNSReconcileRefresh(ctx context.Context, cfg *config.Config, trigger dnsReconcileTrigger) {
	if cfg == nil || !cfg.BackgroundWorkersAllowed() || cfg.PreserveExternalNSD || trigger == nil {
		return
	}
	go runDNSReconcileRefresh(ctx, cfg, trigger, 30*time.Minute)
}
func runDNSReconcileRefresh(ctx context.Context, cfg *config.Config, trigger dnsReconcileTrigger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if cfg.BackgroundWorkersAllowed() && !cfg.PreserveExternalNSD {
				trigger.Trigger()
			}
		}
	}
}
