package agentd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
)

func TestBackupSchedulerReadinessRequiresIndependentInstalledEnabledActiveUnit(t *testing.T) {
	for _, fail := range []string{"cat", "is-enabled", "is-active", ""} {
		t.Run("fail_"+fail, func(t *testing.T) {
			calls := []string{}
			manager := &ApplyManager{cfg: &config.Config{}, run: func(_ context.Context, command string, args ...string) error {
				calls = append(calls, strings.Join(args, " "))
				if command != "/usr/bin/systemctl" {
					t.Fatal("unexpected executable")
				}
				if len(args) > 0 && args[0] == fail {
					return errors.New("controlled scheduler failure")
				}
				return nil
			}}
			err := manager.probeBackupScheduler(t.Context())
			if (fail != "") != (err != nil) {
				t.Fatal("scheduler readiness hid installation/state failure", fail, err)
			}
			if fail == "" && len(calls) != 3 {
				t.Fatal("not all scheduler proofs checked", calls)
			}
		})
	}
	for _, cfg := range []*config.Config{nil, {WorkersHeld: true}, {DevMode: true}, {Quarantined: true}} {
		manager := &ApplyManager{cfg: cfg, run: func(context.Context, string, ...string) error { t.Fatal("held mode consulted scheduler"); return nil }}
		if err := manager.probeBackupScheduler(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
