package main

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
)

type dnsRefreshCounter struct{ calls atomic.Int64 }

func (c *dnsRefreshCounter) Trigger() { c.calls.Add(1) }
func TestDNSRefreshSchedulesOnlyAllowedAuthoritativeMode(t *testing.T) {
	for _, mode := range []string{"active", "held", "quarantined", "development", "external"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := &config.Config{}
				switch mode {
				case "held":
					cfg.WorkersHeld = true
				case "quarantined":
					cfg.Quarantined = true
				case "development":
					cfg.DevMode = true
				case "external":
					cfg.PreserveExternalNSD = true
				}
				ctx, cancel := context.WithCancel(t.Context())
				counter := &dnsRefreshCounter{}
				startDNSReconcileRefresh(ctx, cfg, counter)
				synctest.Wait()
				time.Sleep(61 * time.Minute)
				synctest.Wait()
				cancel()
				synctest.Wait()
				if mode == "active" {
					if counter.calls.Load() != 2 {
						t.Fatalf("refreshes=%d", counter.calls.Load())
					}
				} else if counter.calls.Load() != 0 {
					t.Fatal("held or external mode scheduled DNS side effects")
				}
			})
		})
	}
}
