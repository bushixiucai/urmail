package main

import (
	"context"
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
			// Exercise the actual helper entry points with unavailable dependencies:
			// none may open a lease, DNS/SMTP request or privileged operation.
			startMailTelemetry(t.Context(), cfg, nil, zap.NewNop())
			startMailObservability(t.Context(), cfg, nil, zap.NewNop())
			service := startDomainOnboarding(t.Context(), cfg, nil, nil, nil, zap.NewNop())
			if service == nil {
				t.Fatal("API preparation capability was removed with worker hold")
			}

			select {
			case <-called:
				t.Fatal("disabled worker callback ran")
			case <-time.After(20 * time.Millisecond):
			}
		})
	}
}
