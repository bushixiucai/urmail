package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

// auditLines reads the upgrade audit trail of a test maintenance root.
func auditLines(t *testing.T, rootDir string) []string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(rootDir, maintenance.DefaultUpgradeAuditFile))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(payload), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func TestBreakGlassReleaseAlwaysLeavesAuditTrail(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()

	env.createV2Fence(t, []string{"postfix.service"})

	if err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir, "--break-glass", "--ack-risk"}); err != nil {
		t.Fatalf("break-glass release failed: %v", err)
	}
	if maintenance.IsUpgradeHeld(env.maintDir) {
		t.Fatal("fence must be cleared after successful break-glass release")
	}

	lines := auditLines(t, env.maintDir)
	if len(lines) < 2 {
		t.Fatalf("break-glass release must leave authorization and outcome audit lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], `"action":"release-authorized"`) || !strings.Contains(lines[0], `"mode":"break-glass"`) {
		t.Fatalf("first audit line must authorize the break-glass release: %s", lines[0])
	}
	if !strings.Contains(lines[0], `"generation":155`) {
		t.Fatalf("authorization line must bind the fence generation: %s", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], `"action":"release-released"`) {
		t.Fatalf("last audit line must record the release outcome: %s", lines[len(lines)-1])
	}
}

func TestBreakGlassReleaseRefusedWhenAuditCannotBeWritten(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()

	env.createV2Fence(t, []string{"postfix.service"})
	// Make the audit path unusable: an exception release without a trail is refused.
	if err := os.Mkdir(filepath.Join(env.maintDir, maintenance.DefaultUpgradeAuditFile), 0o700); err != nil {
		t.Fatal(err)
	}

	err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir, "--break-glass", "--ack-risk"})
	if err == nil || !strings.Contains(err.Error(), "cannot record break-glass release audit") {
		t.Fatalf("expected audit-failure refusal, got: %v", err)
	}
	if !maintenance.IsUpgradeHeld(env.maintDir) {
		t.Fatal("fence must be retained when the audit record cannot be written")
	}
}

func TestDefaultReleaseLeavesNoBreakGlassAudit(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()

	env.createV2Fence(t, []string{"postfix.service"})
	env.writeReadiness(t, env.key, nil)

	if err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir}); err != nil {
		t.Fatalf("default release failed: %v", err)
	}
	// BK-03: the default path writes exactly one completed-release audit line
	// and never a break-glass authorization line.
	lines := auditLines(t, env.maintDir)
	if len(lines) != 1 {
		t.Fatalf("default release must write exactly one completed-release audit line, got: %v", lines)
	}
	if !strings.Contains(lines[0], `"action":"release-completed"`) || !strings.Contains(lines[0], `"mode":"default"`) {
		t.Fatalf("default release audit line must record completion: %s", lines[0])
	}
	if strings.Contains(strings.Join(lines, "\n"), `"mode":"break-glass"`) {
		t.Fatalf("default release must not write break-glass audit lines: %v", lines)
	}
}
