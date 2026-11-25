package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigPrepareRejectsBeforeAnyPrivilegedWork(t *testing.T) {
	// The privilege check is intentionally not overridden by URMAIL_ALLOW_TEST_USER.
	t.Setenv("URMAIL_ALLOW_TEST_USER", "1")
	err := runConfigPrepare(context.Background(), []string{"--force"})
	if err == nil || (!strings.Contains(err.Error(), "requires root") && !strings.Contains(err.Error(), "usage:")) {
		t.Fatalf("unexpected prepare argument result: %v", err)
	}
}

func TestFirstBootPrepareAllowsNonRootButRejectsUnknownFlags(t *testing.T) {
	t.Setenv("URMAIL_ALLOW_TEST_USER", "1")
	// The container identity owns its config volume, so first boot may run
	// unprivileged; any later failure must not be the root gate.
	if err := runConfigPrepare(context.Background(), []string{"--first-boot"}); err != nil && strings.Contains(err.Error(), "requires root") {
		t.Fatalf("first-boot refused the container identity: %v", err)
	}
	for _, args := range [][]string{{"--first-boot", "--skip-preflight"}, {"--skip-backup"}} {
		err := runConfigPrepare(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("args %v were accepted: %v", args, err)
		}
	}
	// The fenced upgrade path stays root-only.
	if err := runConfigPrepare(context.Background(), []string{}); err == nil || !strings.Contains(err.Error(), "requires root") {
		t.Fatalf("fenced prepare no longer requires root: %v", err)
	}
}

func TestFirstBootPrepareRefusesActiveUpgradeFence(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("first-boot fence refusal is checked after the root gate")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "upgrade.hold"), []byte("held\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("URMAIL_MAINTENANCE_ROOT", root)
	err := runConfigPrepare(context.Background(), []string{"--first-boot"})
	if err == nil || !strings.Contains(err.Error(), "upgrade fence is active") {
		t.Fatalf("active fence result: %v", err)
	}
}
