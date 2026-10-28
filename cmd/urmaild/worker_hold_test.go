package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/config"
)

func TestSideEffectWorkersDoNotStartInMaintenanceRestoreOrDevelopment(t *testing.T) {
	for name, cfg := range map[string]*config.Config{"maintenance": {WorkersHeld: true}, "restore": {Quarantined: true}, "development": {DevMode: true}} {
		t.Run(name, func(t *testing.T) {
			called := make(chan struct{}, 2)
			if startBackgroundWorker(t.Context(), cfg, func(context.Context) { called <- struct{}{} }) {
				t.Fatal("side-effect worker started")
			}
			if startRenewalWorker(t.Context(), cfg, func(context.Context) error { called <- struct{}{}; return nil }, zap.NewNop()) {
				t.Fatal("renewal started")
			}
			// Exercise the actual helper entry points with unavailable dependencies:
			// none may open a lease, DNS/SMTP/payment request or privileged operation.
			startPaymentRecovery(t.Context(), cfg, nil, nil, "", nil, zap.NewNop())
			startMailTelemetry(t.Context(), cfg, nil, zap.NewNop())
			startMailObservability(t.Context(), cfg, nil, zap.NewNop())
			service := startDomainOnboarding(t.Context(), cfg, nil, nil, nil, zap.NewNop())
			if service == nil {
				t.Fatal("API preparation capability was removed with worker hold")
			}
			if _, err := newBackupWorker(cfg, nil, "invalid-age-recipient", "https://external.invalid"); err == nil || !strings.Contains(err.Error(), "disabled") {
				t.Fatalf("offsite worker touched configuration: %v", err)
			}
			select {
			case <-called:
				t.Fatal("disabled worker callback ran")
			case <-time.After(20 * time.Millisecond):
			}
		})
	}
}
func TestRenewalWorkerStartsOnlyAfterOriginalModeRestored(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	called := make(chan struct{}, 1)
	if !startRenewalWorker(ctx, &config.Config{}, func(context.Context) error { called <- struct{}{}; return nil }, zap.NewNop()) {
		t.Fatal("normal worker did not start")
	}
	select {
	case <-called:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("normal renewal never ran")
	}
}
