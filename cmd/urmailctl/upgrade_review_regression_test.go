package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/maintenance"
)

func TestReleaseRuntimeFailureRefencesAllWriters(t *testing.T) {
	env := newReadinessTestEnv(t)
	env.createV2Fence(t, []string{"postfix.service"})
	env.writeReadiness(t, env.key, nil)
	active := map[string]bool{}
	reset := setReadinessUnitMock(t, active, "")
	defer reset()
	resetVersion := setMigrationVersionVerifierForTest(t)
	defer resetVersion()
	activeReleaseRuntimeVerifier = func(context.Context, *config.Config, string) error {
		if maintenance.IsUpgradeHeld(env.maintDir) || !active["postfix.service"] {
			t.Fatal("probe must run after controlled start")
		}
		active["dovecot.service"] = true // dependency started outside original receipt
		return errors.New("injected protocol failure")
	}
	err := releaseUpgradeInternal(t.Context(), env.maintDir, env.dropInDir, 0, false, false, false)
	if err == nil || !strings.Contains(err.Error(), "protocol readiness") {
		t.Fatalf("expected protocol failure: %v", err)
	}
	if !maintenance.IsUpgradeHeld(env.maintDir) {
		t.Fatal("hold not restored")
	}
	for unit, running := range active {
		if running {
			t.Fatalf("writer left running: %s", unit)
		}
	}
	guards, err := maintenance.CheckDropInGuards(env.dropInDir, maintenance.DropInGuardedUnits)
	if err != nil {
		t.Fatal(err)
	}
	for unit, present := range guards {
		if !present {
			t.Fatalf("guard not restored: %s", unit)
		}
	}
}

func TestReleaseRejectsPreparedManifestChangedSinceSigning(t *testing.T) {
	env := newReadinessTestEnv(t)
	env.createV2Fence(t, nil)
	env.writeReadiness(t, env.key, nil)
	reset := setReadinessUnitMock(t, map[string]bool{}, "")
	defer reset()
	resetVersion := setMigrationVersionVerifierForTest(t)
	defer resetVersion()
	activeAgentConfigStatus = func(context.Context) (*readinessAgentSnapshot, error) {
		return &readinessAgentSnapshot{Step: "prepared", Desired: "gen-155", ManifestSHA256: strings.Repeat("b", 64)}, nil
	}
	if err := releaseUpgradeInternal(t.Context(), env.maintDir, env.dropInDir, 0, false, false, false); err == nil || !strings.Contains(err.Error(), "changed since signing") {
		t.Fatalf("expected manifest drift refusal: %v", err)
	}
	if !maintenance.IsUpgradeHeld(env.maintDir) {
		t.Fatal("hold cleared")
	}
}

func TestReleaseRejectsAbnormalHoldWithoutRemovingGuards(t *testing.T) {
	for _, kind := range []string{"directory", "dangling-symlink"} {
		t.Run(kind, func(t *testing.T) {
			env := newReadinessTestEnv(t)
			hold := filepath.Join(env.maintDir, maintenance.DefaultUpgradeHoldFile)
			var err error
			if kind == "directory" {
				err = os.Mkdir(hold, 0700)
			} else {
				err = os.Symlink("missing", hold)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = maintenance.InstallDropInGuards(env.dropInDir, hold, maintenance.DropInGuardedUnits); err != nil {
				t.Fatal(err)
			}
			if err = releaseUpgradeInternal(t.Context(), env.maintDir, env.dropInDir, 0, false, false, false); err == nil {
				t.Fatal("accepted abnormal hold")
			}
			guards, err := maintenance.CheckDropInGuards(env.dropInDir, maintenance.DropInGuardedUnits)
			if err != nil {
				t.Fatal(err)
			}
			for unit, present := range guards {
				if !present {
					t.Fatalf("removed guard %s", unit)
				}
			}
			if !maintenance.IsUpgradeHeld(env.maintDir) {
				t.Fatal("abnormal marker must fail closed for all writer gates")
			}
		})
	}
}

func TestReadinessRequiresBothFenceDigests(t *testing.T) {
	for _, field := range []string{"hold", "receipt", "both", "malformed"} {
		t.Run(field, func(t *testing.T) {
			env := newReadinessTestEnv(t)
			env.createV2Fence(t, nil)
			env.writeReadiness(t, env.key, func(doc *upgradeReadinessDoc) {
				if field == "hold" || field == "both" {
					doc.HoldSHA256 = ""
				}
				if field == "receipt" || field == "both" {
					doc.ReceiptSHA256 = ""
				}
				if field == "malformed" {
					doc.HoldSHA256 = strings.Repeat("z", 64)
				}
			})
			if _, err := verifyReadinessForRelease(t.Context(), env.maintDir, releaseSigningKeyPath); err == nil {
				t.Fatal("accepted incomplete fence binding")
			}
		})
	}
}

func TestFenceStopsWriterStartedAfterDiscovery(t *testing.T) {
	env := newReadinessTestEnv(t)
	active := map[string]bool{}
	lateStart := false
	reset := setBackupUnitActionForTest(func(_ context.Context, action, unit string) (bool, error) {
		switch action {
		case "state":
			if unit == "urmaild-agent.service" && !lateStart {
				active["postfix@-.service"] = true
				lateStart = true
			}
			return active[unit], nil
		case "stop":
			active[unit] = false
		}
		return false, nil
	})
	defer reset()
	if err := runUpgrade(t.Context(), []string{"fence", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir}); err != nil {
		t.Fatal(err)
	}
	if active["postfix@-.service"] {
		t.Fatal("fence succeeded with late-started writer active")
	}
}

func TestFenceFinalSweepRejectsReactivatedWriter(t *testing.T) {
	env := newReadinessTestEnv(t)
	observations := 0
	reset := setBackupUnitActionForTest(func(_ context.Context, action, unit string) (bool, error) {
		// Reappearance after that writer's individual stop verification must be
		// caught by the independent final full-set sweep.
		if action == "state" && unit == "postfix@-.service" {
			observations++
			if observations == 3 {
				return true, nil
			}
		}
		return false, nil
	})
	defer reset()
	if err := runUpgrade(t.Context(), []string{"fence", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir}); err == nil {
		t.Fatal("fence accepted active writer")
	}
}
