package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/maintenance"
)

// readinessTestEnv installs isolated hooks for readiness/release tests and
// returns a restore function.
type readinessTestEnv struct {
	maintDir  string
	dropInDir string
	key       ed25519.PrivateKey
}

func newReadinessTestEnv(t *testing.T) *readinessTestEnv {
	t.Helper()
	allowUpgradeTestUser(t)

	env := &readinessTestEnv{
		maintDir:  t.TempDir(),
		dropInDir: t.TempDir(),
	}
	_, env.key, _ = ed25519.GenerateKey(rand.Reader)
	keyPath := filepath.Join(t.TempDir(), "release-signing.key")
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(env.key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("URMAIL_MAINTENANCE_ROOT", env.maintDir)
	envFile := filepath.Join(env.maintDir, "urmail.env")
	if err := os.WriteFile(envFile, []byte("URMAIL_DATABASE_URL=postgres://app@localhost/urmail\nURMAIL_DATABASE_URL_PLATFORM=postgres://platform@localhost/urmail\nURMAIL_QUOTE_HMAC_KEY=test-key\nURMAIL_POLICY_ENABLED=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("URMAIL_ENV_FILE", envFile)

	prevRuntime := activeReleaseRuntimeVerifier
	activeReleaseRuntimeVerifier = func(context.Context, *config.Config, string) error { return nil }
	t.Cleanup(func() { activeReleaseRuntimeVerifier = prevRuntime })
	prevAgent, prevVersion, prevNow, prevWindow, prevKey := activeAgentConfigStatus, activeMigrationVersion, readinessNow, readinessFreshnessWindow, releaseSigningKeyPath
	activeAgentConfigStatus = func(_ context.Context) (*readinessAgentSnapshot, error) {
		return &readinessAgentSnapshot{Ready: false, Desired: "gen-155", Applied: "old", Step: "prepared", ManifestSHA256: strings.Repeat("a", 64), AppliedAt: time.Now().UTC()}, nil
	}
	activeMigrationVersion = func(_ context.Context) (uint64, error) { return 155, nil }
	readinessNow = func() time.Time { return time.Now().UTC() }
	releaseSigningKeyPath = keyPath
	t.Cleanup(func() {
		activeAgentConfigStatus, activeMigrationVersion, readinessNow, releaseSigningKeyPath = prevAgent, prevVersion, prevNow, prevKey
		readinessFreshnessWindow = prevWindow
	})
	return env
}

func (e *readinessTestEnv) createV2Fence(t *testing.T, units []string) {
	t.Helper()
	meta := maintenance.UpgradeFenceMeta{
		Version:    2,
		CreatedAt:  time.Now().UTC(),
		Reason:     "readiness test fence",
		Operator:   "test-operator",
		Generation: 155,
		Units:      units,
	}
	if err := maintenance.CreateUpgradeFence(e.maintDir, meta); err != nil {
		t.Fatalf("CreateUpgradeFence failed: %v", err)
	}
}

// writeReadiness writes a signed readiness receipt bound to the live fence,
// optionally mutated before signing.
func (e *readinessTestEnv) writeReadiness(t *testing.T, key ed25519.PrivateKey, mutate func(*upgradeReadinessDoc)) {
	t.Helper()
	holdRaw, receiptRaw, err := maintenance.ReadUpgradeFenceRaw(e.maintDir)
	if err != nil {
		t.Fatal(err)
	}
	doc := upgradeReadinessDoc{
		Version:       readinessRecordVersion,
		RecordedAt:    readinessNow().UTC(),
		HoldSHA256:    sha256Sum(holdRaw),
		ReceiptSHA256: sha256Sum(receiptRaw),
		Generation:    155,
		AgentStatus:   &readinessAgentSnapshot{Ready: false, Desired: "gen-155", Applied: "old", Step: "prepared", ManifestSHA256: strings.Repeat("a", 64)},
	}
	if mutate != nil {
		mutate(&doc)
	}
	payload, err := canonicalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(e.maintDir, DefaultReadinessRecordName)
	if err = os.WriteFile(recordPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(recordPath+".sig", []byte(hex.EncodeToString(ed25519.Sign(key, payload))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setReadinessUnitMock(t *testing.T, active map[string]bool, failStart string) func() {
	t.Helper()
	return setBackupUnitActionForTest(func(_ context.Context, action, unit string) (bool, error) {
		switch action {
		case "state":
			return active[unit], nil
		case "stop":
			active[unit] = false
			return false, nil
		case "start":
			if unit == failStart {
				return false, errors.New("simulated start failure")
			}
			active[unit] = true
			return true, nil
		default:
			return false, errors.New("unknown unit action")
		}
	})
}

func setMigrationVersionVerifierForTest(t *testing.T) func() {
	t.Helper()
	return setMigrationVerifierForTest(func(_ context.Context, _ string) error { return nil })
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	done := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(reader)
		done <- string(raw)
	}()
	fn()
	_ = writer.Close()
	os.Stdout = original
	return <-done
}

func TestReadinessV2FenceBindsHoldDigestAndGeneration(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{"postfix.service": true}, "")
	defer reset()

	if err := runUpgrade(ctx, []string{"fence", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir}); err != nil {
		t.Fatalf("fence failed: %v", err)
	}
	holdRaw, receiptRaw, err := maintenance.ReadUpgradeFenceRaw(env.maintDir)
	if err != nil {
		t.Fatal(err)
	}
	var receipt maintenance.UpgradeReceipt
	if err = json.Unmarshal(receiptRaw, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Version != 2 || receipt.Generation != 155 {
		t.Fatalf("expected v2 receipt with generation 155, got %+v", receipt)
	}
	if receipt.HoldSHA256 != sha256Sum(holdRaw) {
		t.Fatal("receipt hold_sha256 does not bind live hold bytes")
	}
}

func TestReadinessRecordAndDefaultReleaseRoundTrip(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	units := map[string]bool{"postfix.service": true}
	reset := setReadinessUnitMock(t, units, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()

	env.createV2Fence(t, []string{"postfix.service"})

	// Default release without a readiness receipt MUST fail listing evidence.
	defaultErr := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir})
	if defaultErr == nil || !strings.Contains(defaultErr.Error(), "readiness") || !strings.Contains(defaultErr.Error(), "--break-glass --ack-risk") {
		t.Fatalf("expected default release rejection listing readiness evidence, got: %v", defaultErr)
	}
	if !maintenance.IsUpgradeHeld(env.maintDir) {
		t.Fatal("fence must be retained after readiness rejection")
	}

	// Record readiness via the real CLI path (real ed25519 signing).
	err := runUpgrade(ctx, []string{"readiness-record", "--root-dir", env.maintDir, "--key", releaseSigningKeyPath})
	if err != nil {
		t.Fatalf("readiness-record failed: %v", err)
	}
	recordPath := filepath.Join(env.maintDir, DefaultReadinessRecordName)
	info, statErr := os.Stat(recordPath)
	if statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("readiness receipt must exist root-only 0600, got %v %v", info, statErr)
	}
	if _, statErr = os.Stat(recordPath + ".sig"); statErr != nil {
		t.Fatalf("detached signature missing: %v", statErr)
	}

	// Default release consumes the signed receipt.
	if err = runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir}); err != nil {
		t.Fatalf("default release failed: %v", err)
	}
	if maintenance.IsUpgradeHeld(env.maintDir) {
		t.Fatal("fence must be cleared after successful release")
	}
	if !units["postfix.service"] {
		t.Fatal("receipt unit must be restarted and verified active")
	}
}

func TestReadinessRecordRejectsAgentNotConverged(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{}, "")
	defer reset()
	env.createV2Fence(t, nil)
	prev := activeAgentConfigStatus
	activeAgentConfigStatus = func(_ context.Context) (*readinessAgentSnapshot, error) {
		return &readinessAgentSnapshot{Ready: true, Degraded: true, Desired: "a", Applied: "b"}, nil
	}
	err := runUpgrade(ctx, []string{"readiness-record", "--root-dir", env.maintDir, "--key", releaseSigningKeyPath})
	activeAgentConfigStatus = prev
	if err == nil || !strings.Contains(err.Error(), "not ready/converged") {
		t.Fatalf("expected non-converged rejection, got: %v", err)
	}
}

func TestReadinessRecordRejectsGenerationFlagDrift(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{}, "")
	defer reset()
	env.createV2Fence(t, nil)
	err := runUpgrade(ctx, []string{"readiness-record", "--generation", "999", "--root-dir", env.maintDir, "--key", releaseSigningKeyPath})
	if err == nil || !strings.Contains(err.Error(), "generation drift") {
		t.Fatalf("expected generation drift rejection, got: %v", err)
	}
}

func TestReadinessForgedSignatureAndTamperRejected(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{"postfix.service": true}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()
	env.createV2Fence(t, []string{"postfix.service"})

	_, foreignKey, _ := ed25519.GenerateKey(rand.Reader)
	env.writeReadiness(t, foreignKey, nil)
	err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir})
	if err == nil || !strings.Contains(err.Error(), "signature verification FAILED") {
		t.Fatalf("expected forged signature rejection, got: %v", err)
	}

	// Tampered payload: valid signature over different bytes.
	env.writeReadiness(t, env.key, func(doc *upgradeReadinessDoc) { doc.Generation = 154 })
	tampered, _ := os.ReadFile(filepath.Join(env.maintDir, DefaultReadinessRecordName))
	_ = os.WriteFile(filepath.Join(env.maintDir, DefaultReadinessRecordName), append(tampered, byte(' ')), 0o600)
	err = runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir})
	if err == nil || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("expected tampered payload rejection, got: %v", err)
	}
	if !maintenance.IsUpgradeHeld(env.maintDir) {
		t.Fatal("fence must be retained after signature failures")
	}
}

func TestReadinessDigestMismatchRejected(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{"postfix.service": true}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()
	env.createV2Fence(t, []string{"postfix.service"})
	env.writeReadiness(t, env.key, nil)

	// Live hold changes after the receipt was recorded.
	holdPath := filepath.Join(env.maintDir, maintenance.DefaultUpgradeHoldFile)
	raw, _ := os.ReadFile(holdPath)
	_ = os.WriteFile(holdPath, append(raw, byte('\n')), 0o600)

	err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir})
	if err == nil || !strings.Contains(err.Error(), "hold digest mismatch") {
		t.Fatalf("expected hold digest mismatch rejection, got: %v", err)
	}
}

func TestReadinessGenerationDriftAtReleaseRejected(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{"postfix.service": true}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()
	env.createV2Fence(t, []string{"postfix.service"})
	env.writeReadiness(t, env.key, nil)

	prev := activeMigrationVersion
	activeMigrationVersion = func(_ context.Context) (uint64, error) { return 156, nil }
	err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir})
	activeMigrationVersion = prev
	if err == nil || !strings.Contains(err.Error(), "generation drift") {
		t.Fatalf("expected generation drift rejection, got: %v", err)
	}
}

func TestReadinessStaleReceiptRejected(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{"postfix.service": true}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()
	env.createV2Fence(t, []string{"postfix.service"})
	env.writeReadiness(t, env.key, func(doc *upgradeReadinessDoc) {
		doc.RecordedAt = readinessNow().Add(-25 * time.Hour)
	})

	err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir})
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("expected stale receipt rejection, got: %v", err)
	}
}

func TestBreakGlassRefusesNonReceiptActiveUnitsFailClosed(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	// nginx.service is active but NOT in the fence receipt.
	reset := setReadinessUnitMock(t, map[string]bool{"postfix.service": true, "nginx.service": true}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()
	env.createV2Fence(t, []string{"postfix.service"})

	err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir, "--break-glass", "--ack-risk"})
	if err == nil || !strings.Contains(err.Error(), "non-receipt units active outside upgrade receipt") {
		t.Fatalf("expected break-glass non-receipt rejection, got: %v", err)
	}
	if !strings.Contains(err.Error(), "RESTORED") {
		t.Fatalf("expected restored fence message, got: %v", err)
	}
	if !maintenance.IsUpgradeHeld(env.maintDir) {
		t.Fatal("fence must be retained (fail-closed) after break-glass refusal")
	}
	guards, _ := maintenance.CheckDropInGuards(env.dropInDir, maintenance.DropInGuardedUnits)
	for unit, present := range guards {
		if !present {
			t.Fatalf("guard for %s must be retained after break-glass refusal", unit)
		}
	}
}

func TestReleasePostStartRecheckFailClosed(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	// start "succeeds" but the unit reports inactive afterwards.
	reset := setBackupUnitActionForTest(func(_ context.Context, action, _ string) (bool, error) {
		switch action {
		case "state":
			return false, nil
		case "stop":
			return false, nil
		case "start":
			return true, nil
		default:
			return false, errors.New("unknown unit action")
		}
	})
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()
	env.createV2Fence(t, []string{"postfix.service"})
	env.writeReadiness(t, env.key, nil)

	err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir})
	if err == nil || !strings.Contains(err.Error(), "not active after start command") {
		t.Fatalf("expected post-start recheck failure, got: %v", err)
	}
	if !maintenance.IsUpgradeHeld(env.maintDir) {
		t.Fatal("fence must be restored after post-start recheck failure")
	}
}

func TestReleaseGuardRemovalFailureRestoresFence(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{"postfix.service": true}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()
	env.createV2Fence(t, []string{"postfix.service"})
	env.writeReadiness(t, env.key, nil)

	// Make one guard conf path a directory: os.Remove fails (not ErrNotExist).
	stuck := filepath.Join(env.dropInDir, "dovecot.socket.d", maintenance.DropInFileName)
	if err := os.MkdirAll(stuck, 0o755); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory cannot be removed: os.Remove fails, not ErrNotExist.
	if err := os.WriteFile(filepath.Join(stuck, "child"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir})
	if err == nil || !strings.Contains(err.Error(), "remove drop-in guards") {
		t.Fatalf("expected guard removal failure, got: %v", err)
	}
	if !maintenance.IsUpgradeHeld(env.maintDir) {
		t.Fatal("hold marker must never be removed when guard removal fails (guards-first ordering)")
	}
}

func TestRestoreFenceRejectsMissingMetaAndReceipt(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	// nginx.service active and not in any receipt (receipt absent entirely).
	reset := setReadinessUnitMock(t, map[string]bool{"nginx.service": true}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()

	// Fence with unparseable hold JSON and NO receipt: Meta and Receipt are nil.
	holdPath := filepath.Join(env.maintDir, maintenance.DefaultUpgradeHoldFile)
	if err := os.WriteFile(holdPath, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir, "--break-glass", "--ack-risk"})
	if err == nil || !strings.Contains(err.Error(), "cannot restore protective fence") {
		t.Fatalf("expected explicit restore refusal, got: %v", err)
	}
	if !maintenance.IsUpgradeHeld(env.maintDir) {
		t.Fatal("fence must be retained; empty fence rebuild is forbidden")
	}
}

func TestIdempotentReleaseDoesNotSwallowGuardCleanupError(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()

	// No fence at all; stuck guard conf directory must surface an error.
	stuck := filepath.Join(env.dropInDir, "dovecot.socket.d", maintenance.DropInFileName)
	if err := os.MkdirAll(stuck, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stuck, "child"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir})
	if err == nil || !strings.Contains(err.Error(), "drop-in guard cleanup failed") {
		t.Fatalf("expected idempotent path to surface guard cleanup error, got: %v", err)
	}
}

func TestRollbackGuidanceMentionsReadinessOrBreakGlass(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{}, "")
	defer reset()
	env.createV2Fence(t, nil)

	output := captureStdout(t, func() {
		if err := runUpgrade(ctx, []string{"rollback", "--root-dir", env.maintDir}); err != nil {
			t.Errorf("rollback failed: %v", err)
		}
	})
	if !strings.Contains(output, "readiness-record") || !strings.Contains(output, "--break-glass --ack-risk") {
		t.Fatalf("rollback guidance must offer readiness-record or explicit break-glass, got:\n%s", output)
	}
}

func TestReleaseDefaultPathRefusesNonReceiptActiveUnitsFailClosed(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	// nginx.service active outside the fence receipt; default path (no flags).
	reset := setReadinessUnitMock(t, map[string]bool{"postfix.service": true, "nginx.service": true}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()
	env.createV2Fence(t, []string{"postfix.service"})
	env.writeReadiness(t, env.key, nil)

	err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir})
	if err == nil || !strings.Contains(err.Error(), "non-receipt units active outside upgrade receipt") {
		t.Fatalf("expected default-path non-receipt fail-closed, got: %v", err)
	}
	if !maintenance.IsUpgradeHeld(env.maintDir) {
		t.Fatal("fence must be retained after default-path non-receipt refusal")
	}
	guards, _ := maintenance.CheckDropInGuards(env.dropInDir, maintenance.DropInGuardedUnits)
	for unit, present := range guards {
		if !present {
			t.Fatalf("guard for %s must be retained after refusal", unit)
		}
	}
}

func TestReadinessRecordReplaceAtomicallyRewrites(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{"postfix.service": true}, "")
	defer reset()
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()
	env.createV2Fence(t, []string{"postfix.service"})

	recordArgs := make([]string, 0, 6)
	recordArgs = append(recordArgs, "readiness-record", "--root-dir", env.maintDir, "--key", releaseSigningKeyPath)
	if err := runUpgrade(ctx, recordArgs); err != nil {
		t.Fatalf("initial readiness-record failed: %v", err)
	}
	// Without --replace the second record must fail (exclusive create).
	if err := runUpgrade(ctx, recordArgs); err == nil {
		t.Fatal("expected second readiness-record without --replace to fail")
	}
	// With --replace it succeeds atomically; no tmp residue.
	if err := runUpgrade(ctx, append(recordArgs, "--replace")); err != nil {
		t.Fatalf("readiness-record --replace failed: %v", err)
	}
	entries, _ := os.ReadDir(env.maintDir)
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".replace.tmp") {
			t.Fatalf("tmp residue left behind: %s", entry.Name())
		}
	}
	// The replaced record still verifies and releases.
	if err := runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir}); err != nil {
		t.Fatalf("release after --replace failed: %v", err)
	}
}

func TestReadinessZeroGenerationRejected(t *testing.T) {
	env := newReadinessTestEnv(t)
	ctx := t.Context()
	reset := setReadinessUnitMock(t, map[string]bool{}, "")
	defer reset()
	env.createV2Fence(t, nil)

	// Record-time: live generation 0 must be explicitly refused.
	prev := activeMigrationVersion
	activeMigrationVersion = func(_ context.Context) (uint64, error) { return 0, nil }
	err := runUpgrade(ctx, []string{"readiness-record", "--root-dir", env.maintDir, "--key", releaseSigningKeyPath})
	if err == nil || !strings.Contains(err.Error(), "live migration version is 0") {
		t.Fatalf("expected zero generation rejection at record, got: %v", err)
	}
	// Verify-time: receipt without generation and live 0 are both listed.
	env.writeReadiness(t, env.key, func(doc *upgradeReadinessDoc) { doc.Generation = 0 })
	resetVerifier := setMigrationVersionVerifierForTest(t)
	defer resetVerifier()
	activeMigrationVersion = func(_ context.Context) (uint64, error) { return 0, nil }
	err = runUpgrade(ctx, []string{"release", "--root-dir", env.maintDir, "--drop-in-dir", env.dropInDir})
	activeMigrationVersion = prev
	if err == nil || !strings.Contains(err.Error(), "no recorded generation") || !strings.Contains(err.Error(), "live migration version is 0") {
		t.Fatalf("expected zero generation problems at release, got: %v", err)
	}
}

func TestReadinessResumeGuidanceUpdated(t *testing.T) {
	allowUpgradeTestUser(t)
	// resume --release rejection must point at readiness-record instead of
	// break-glass-only guidance.
	err := runUpgrade(t.Context(), []string{"resume", "--release"})
	if err == nil || !strings.Contains(err.Error(), "readiness-record") {
		t.Fatalf("resume guidance must point at readiness-record, got: %v", err)
	}
}
