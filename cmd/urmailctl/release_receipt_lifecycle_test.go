package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

// readySnapshot builds a converged, ready live agent snapshot for one
// generation string.
func readySnapshot(generation string) *readinessAgentSnapshot {
	return &readinessAgentSnapshot{Ready: true, Degraded: false, Desired: generation, Applied: generation, Step: "ready"}
}

func TestReleaseAfterAgentReadyRewritesReceipt(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()

	env.createV2Fence(t, []string{"postfix.service"})
	env.writeReadiness(t, env.key, nil)

	recordPath := filepath.Join(maintenance.Root(env.maintDir), DefaultReadinessRecordName)
	preparedPayload, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var preparedDoc upgradeReadinessDoc
	if err = json.Unmarshal(preparedPayload, &preparedDoc); err != nil {
		t.Fatal(err)
	}
	if preparedDoc.AgentStatus.Ready || preparedDoc.AgentStatus.Step != "prepared" {
		t.Fatalf("fixture receipt must bind a prepared snapshot, got: %+v", preparedDoc.AgentStatus)
	}

	// The live agent has reached ready on the same desired generation the
	// receipt binds; the release must re-sign the receipt with that state.
	prev := activeLiveAgentStatus
	activeLiveAgentStatus = func(context.Context) (*readinessAgentSnapshot, error) {
		return readySnapshot(preparedDoc.AgentStatus.Desired), nil
	}
	t.Cleanup(func() { activeLiveAgentStatus = prev })

	if err = runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir}); err != nil {
		t.Fatalf("release failed: %v", err)
	}

	payload, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	sigRaw, err := os.ReadFile(recordPath + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	var doc upgradeReadinessDoc
	if err = json.Unmarshal(payload, &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.AgentStatus.Ready || doc.AgentStatus.Step != "ready" || doc.AgentStatus.Desired != preparedDoc.AgentStatus.Desired {
		t.Fatalf("receipt must bind the live ready snapshot, got: %+v", doc.AgentStatus)
	}
	if doc.HoldSHA256 != preparedDoc.HoldSHA256 || doc.Generation != preparedDoc.Generation {
		t.Fatal("rewritten receipt must keep the historical fence binding")
	}
	decoded, decodeErr := hex.DecodeString(strings.TrimSpace(string(sigRaw)))
	if decodeErr != nil || len(decoded) != ed25519.SignatureSize {
		t.Fatalf("rewritten signature malformed: %v", decodeErr)
	}
	if !ed25519.Verify(env.key.Public().(ed25519.PublicKey), payload, decoded) {
		t.Fatal("rewritten receipt signature does not verify against the new payload")
	}
}

// TestReleaseWithCrashedAgentKeepsPreparedReceipt covers the crash window: the
// live agent never answers the status query, so the receipt must stay exactly
// as signed (prepared) and the release must still complete with its audit.
func TestReleaseWithCrashedAgentKeepsPreparedReceipt(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()

	env.createV2Fence(t, []string{"postfix.service"})
	env.writeReadiness(t, env.key, nil)

	recordPath := filepath.Join(maintenance.Root(env.maintDir), DefaultReadinessRecordName)
	preparedPayload, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	prev := activeLiveAgentStatus
	activeLiveAgentStatus = func(context.Context) (*readinessAgentSnapshot, error) {
		return nil, os.ErrDeadlineExceeded // agent crashed before answering
	}
	t.Cleanup(func() { activeLiveAgentStatus = prev })

	if err = runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir}); err != nil {
		t.Fatalf("release must not fail when only the receipt rewrite is unavailable: %v", err)
	}
	payload, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != string(preparedPayload) {
		t.Fatal("crashed agent must leave the signed receipt byte-identical (no rewrite)")
	}
}

// TestReleaseAuditFileBindsReleasedState: the completed-release audit file must
// exist after release and its digests must bind the exact hold bytes, signed
// readiness receipt bytes and guard set that were live at release time.
func TestReleaseAuditFileBindsReleasedState(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()

	env.createV2Fence(t, []string{"postfix.service"})
	env.writeReadiness(t, env.key, nil)

	// Install the same drop-in guard a real fenced site has, so the audit's
	// guard summary binds a non-empty, verifiable set.
	if _, err := maintenance.InstallDropInGuards(env.dropInDir, filepath.Join(env.maintDir, maintenance.DefaultUpgradeHoldFile), []string{"postfix.service"}); err != nil {
		t.Fatalf("install fixture guard: %v", err)
	}

	rawHold, rawFenceReceipt, err := maintenance.ReadUpgradeFenceRaw(env.maintDir)
	if err != nil {
		t.Fatal(err)
	}
	readinessPayload, err := os.ReadFile(filepath.Join(maintenance.Root(env.maintDir), DefaultReadinessRecordName))
	if err != nil {
		t.Fatal(err)
	}

	prev := activeLiveAgentStatus
	activeLiveAgentStatus = func(context.Context) (*readinessAgentSnapshot, error) { return nil, os.ErrDeadlineExceeded }
	t.Cleanup(func() { activeLiveAgentStatus = prev })

	if err = runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir}); err != nil {
		t.Fatalf("release failed: %v", err)
	}

	auditPayload, err := os.ReadFile(filepath.Join(env.maintDir, maintenance.DefaultUpgradeAuditFile))
	if err != nil {
		t.Fatalf("completed-release audit file must exist after release: %v", err)
	}
	var entry maintenance.UpgradeAuditEntry
	if err = json.Unmarshal([]byte(strings.TrimSpace(string(auditPayload))), &entry); err != nil {
		t.Fatalf("audit line must parse: %v", err)
	}
	if entry.Action != "release-completed" || entry.Mode != "default" {
		t.Fatalf("unexpected audit entry: %+v", entry)
	}
	if entry.HoldSHA256 != sha256Sum(rawHold) {
		t.Fatal("audit hold digest must bind the exact released hold bytes")
	}
	if entry.ReceiptSHA256 != sha256Sum(rawFenceReceipt) {
		t.Fatal("audit receipt digest must bind the exact released fence receipt bytes")
	}
	if entry.ReadinessSHA256 != sha256Sum(readinessPayload) {
		t.Fatal("audit readiness digest must bind the exact signed readiness receipt bytes")
	}
	if len(entry.Guards) != 1 || entry.Guards[0] != "postfix.service" {
		t.Fatalf("audit must record the guarded unit set, got: %v", entry.Guards)
	}
	if entry.Generation != 155 {
		t.Fatalf("audit must bind the fence generation, got: %d", entry.Generation)
	}
}

// TestDefaultReleaseAuditFailureRestoresFence: the completed-release record is
// mandatory in every mode; if it cannot be written the fence is restored and
// the release fails closed.
func TestDefaultReleaseAuditFailureRestoresFence(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()

	env.createV2Fence(t, []string{"postfix.service"})
	env.writeReadiness(t, env.key, nil)
	if err := os.Mkdir(filepath.Join(env.maintDir, maintenance.DefaultUpgradeAuditFile), 0o700); err != nil {
		t.Fatal(err)
	}

	err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir})
	if err == nil || !strings.Contains(err.Error(), "cannot record completed-release audit") {
		t.Fatalf("expected completed-release audit refusal, got: %v", err)
	}
	if !maintenance.IsUpgradeHeld(env.maintDir) {
		t.Fatal("fence must be restored when the completed-release audit cannot be written")
	}
}
