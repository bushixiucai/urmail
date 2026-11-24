//go:build urmailreadinesstest

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

type attestFixture struct {
	dir, root, sqlitePath, required, probePath, attestPath, keyPath, pubPath string
	key                                                                      ed25519.PrivateKey
	lockFD                                                                   int
}

func newAttestFixture(t *testing.T) *attestFixture {
	t.Helper()
	f := &attestFixture{dir: t.TempDir()}
	f.root = readinessFence(t, 6)
	f.sqlitePath = filepath.Join(f.dir, "target.sqlite")
	makeSQLiteTarget(t, f.sqlitePath, "original")
	f.required = filepath.Join(f.dir, "required.json")
	if err := os.WriteFile(f.required, []byte(`{"attest":"bound"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secure := secureTempDir(t)
	f.key = priv
	f.keyPath = filepath.Join(secure, "release.key")
	f.pubPath = filepath.Join(secure, "release.pub")
	if err := os.WriteFile(f.keyPath, []byte(hex.EncodeToString(priv)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.pubPath, []byte(hex.EncodeToString(pub)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := f.probeOptions()
	doc, err := probeReadiness(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	f.probePath = filepath.Join(f.dir, "probe.json")
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(f.probePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	f.attestPath = filepath.Join(f.dir, "attest.json")
	lockPath := filepath.Join(f.root, "operation.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lockFile.Close() })
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	f.lockFD = int(lockFile.Fd())
	t.Setenv("URMAIL_ALLOW_TEST_USER", "1")
	t.Setenv("URMAIL_READINESS_TEST_ANCHOR", f.pubPath)
	return f
}

func makeSQLiteTarget(t *testing.T, path, content string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version=6`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS probe_identity(v TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO probe_identity(v) VALUES(?)`, content); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
}

func (f *attestFixture) probeOptions() readinessProbeOptions {
	return readinessProbeOptions{rootDir: f.root, generation: 6, sqlitePath: f.sqlitePath, sqliteVersion: 6, requiredFiles: []string{f.required}}
}

func (f *attestFixture) targetArgs() []string {
	return []string{"--generation", "6", "--root-dir", f.root, "--sqlite", f.sqlitePath, "--sqlite-generation", "6", "--required-file", f.required, "--lock-fd", strconv.Itoa(f.lockFD)}
}

func (f *attestFixture) signArgs() []string {
	return append(f.targetArgs(), "--probe", f.probePath, "--attest", f.attestPath, "--key", f.keyPath)
}

func (f *attestFixture) verifyArgs() []string {
	return append(f.targetArgs(), "--attest", f.attestPath)
}

func TestReadinessAttestSignVerifyLifecycle(t *testing.T) {
	f := newAttestFixture(t)
	ctx := context.Background()
	if err := runReadinessAttestSign(ctx, f.signArgs()); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := runReadinessAttestVerify(ctx, f.verifyArgs()); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := os.Stat(f.attestPath + ".sig"); err != nil {
		t.Fatalf("signature missing: %v", err)
	}

	// Missing attestation is never accepted.
	if err := runReadinessAttestVerify(ctx, append(f.targetArgs(), "--attest", filepath.Join(f.dir, "absent.json"))); err == nil {
		t.Fatal("missing attestation accepted")
	}
	// Wrong trusted anchor.
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(secureTempDir(t), "other.pub")
	if err := os.WriteFile(other, []byte(hex.EncodeToString(otherPub)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("URMAIL_READINESS_TEST_ANCHOR", other)
	if err := runReadinessAttestVerify(ctx, append(f.targetArgs(), "--attest", f.attestPath)); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("wrong anchor accepted: %v", err)
	}
	t.Setenv("URMAIL_READINESS_TEST_ANCHOR", f.pubPath)
	// Tampered signature.
	sigPath := f.attestPath + ".sig"
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]byte{}, sig...)
	if bad[0] == '0' {
		bad[0] = '1'
	} else {
		bad[0] = '0'
	}
	if err := os.WriteFile(sigPath, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runReadinessAttestVerify(ctx, f.verifyArgs()); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("tampered signature accepted: %v", err)
	}
	if err := os.WriteFile(sigPath, sig, 0o600); err != nil {
		t.Fatal(err)
	}

	// State change after signing is rejected even when the caller presents the
	// old attestation unchanged.
	replacement := filepath.Join(f.dir, "replacement.sqlite")
	makeSQLiteTarget(t, replacement, "replacement")
	if err := os.Rename(replacement, f.sqlitePath); err != nil {
		t.Fatal(err)
	}
	if err := runReadinessAttestVerify(ctx, f.verifyArgs()); err == nil || !strings.Contains(err.Error(), "STATE_CHANGED") {
		t.Fatalf("changed state accepted: %v", err)
	}
}

func TestReadinessAttestRefusesUnprovenInputs(t *testing.T) {
	f := newAttestFixture(t)
	ctx := context.Background()
	// A probe record that does not match live state is not signed.
	changed := filepath.Join(f.dir, "changed.json")
	if err := os.WriteFile(f.required, []byte(`{"attest":"changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	live, err := probeReadiness(ctx, f.probeOptions())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(live)
	if err := os.WriteFile(changed, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.required, []byte(`{"attest":"bound"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append(f.targetArgs(), "--probe", changed, "--attest", f.attestPath, "--key", f.keyPath)
	if err := runReadinessAttestSign(ctx, args); err == nil || !strings.Contains(err.Error(), "does not match live state") {
		t.Fatalf("mismatched probe signed: %v", err)
	}
	// A missing signing key is refused and never created.
	absentKey := filepath.Join(f.dir, "absent.key")
	args = append(f.targetArgs(), "--probe", f.probePath, "--attest", f.attestPath, "--key", absentKey)
	if err := runReadinessAttestSign(ctx, args); err == nil {
		t.Fatal("missing key accepted")
	}
	if _, err := os.Stat(absentKey); !os.IsNotExist(err) {
		t.Fatal("signing key was created")
	}
	// Stale attestation (issued outside the freshness window) is refused even
	// with a valid signature.
	if err := runReadinessAttestSign(ctx, f.signArgs()); err != nil {
		t.Fatal(err)
	}
	payloadRaw, err := os.ReadFile(f.attestPath)
	if err != nil {
		t.Fatal(err)
	}
	var payload readinessAttestPayload
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		t.Fatal(err)
	}
	payload.IssuedAt = time.Now().UTC().Add(-readinessFreshnessWindow - time.Hour)
	staleRaw, _ := json.Marshal(payload)
	signature := ed25519.Sign(f.key, staleRaw)
	if err := os.WriteFile(f.attestPath, staleRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.attestPath+".sig", []byte(hex.EncodeToString(signature)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runReadinessAttestVerify(ctx, f.verifyArgs()); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("stale attestation accepted: %v", err)
	}
}

// TestReadinessAttestConsumeOnce proves the fixed consumption entry binds the
// live inputs under the operation lock and refuses a second attempt while the
// original record remains.
func TestReadinessAttestConsumeOnce(t *testing.T) {
	f := newAttestFixture(t)
	ctx := context.Background()
	if err := runReadinessAttestSign(ctx, f.signArgs()); err != nil {
		t.Fatalf("sign: %v", err)
	}
	consumeArgs := func(request string) []string {
		return append(f.targetArgs(), "--attest", f.attestPath, "--request-id", request)
	}
	if err := runReadinessAttestConsume(ctx, consumeArgs("")); err == nil || !strings.Contains(err.Error(), "request-id") {
		t.Fatalf("missing request id accepted: %v", err)
	}
	if err := runReadinessAttestConsume(ctx, consumeArgs("attempt-one")); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	recordPath := filepath.Join(f.root, "upgrade_readiness_consumed.json")
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("consumed record missing: %v", err)
	}
	var record readinessConsumedRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.RequestID != "attempt-one" || record.Generation != 6 || record.HoldSHA256 == "" || record.AttestSHA256 == "" {
		t.Fatalf("unexpected consumed record: %+v", record)
	}
	if err := runReadinessAttestConsume(ctx, consumeArgs("attempt-two")); err == nil || !strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("second consume accepted: %v", err)
	}
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("original record lost after refusal: %v", err)
	}
}

// TestReleaseConsumeGateSameAttempt proves the release-side gate compares the
// consumed record with the live hold/receipt, re-verifies the attestation under
// the inherited lock, and refuses a second release attempt.
func TestReleaseConsumeGateSameAttempt(t *testing.T) {
	f := newAttestFixture(t)
	ctx := context.Background()
	if err := runReadinessAttestSign(ctx, f.signArgs()); err != nil {
		t.Fatal(err)
	}
	if err := runReadinessAttestConsume(ctx, append(f.targetArgs(), "--attest", f.attestPath, "--request-id", "release-one")); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseConsume(ctx, f.root, f.lockFD); err != nil {
		t.Fatalf("release consume gate: %v", err)
	}
	attemptPath := filepath.Join(f.root, "upgrade_release_attempt.json")
	if _, err := os.Stat(attemptPath); err != nil {
		t.Fatalf("release attempt missing: %v", err)
	}
	// An interrupted attempt that did not remove the hold continues under the
	// same lock/consume record instead of masquerading as success.
	if err := verifyReleaseConsume(ctx, f.root, f.lockFD); err != nil {
		t.Fatalf("interrupted same-attempt continuation refused: %v", err)
	}
	if err := markReleaseCompleted(f.root, mustHoldSHA(t, f.root)); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseConsume(ctx, f.root, f.lockFD); err == nil || !strings.Contains(err.Error(), "already released") {
		t.Fatalf("replayed completed release accepted: %v", err)
	}
	if already, err := noHoldReleaseOutcome(f.root, true); err != nil || !already {
		t.Fatalf("no-hold completed attempt not recognized: already=%t err=%v", already, err)
	}
	if already, err := noHoldReleaseOutcome(t.TempDir(), true); err == nil || already {
		t.Fatalf("no-hold without attempt claimed success: already=%t err=%v", already, err)
	}

	// A record from another round must not release this hold.
	other := newAttestFixture(t)
	if err := runReadinessAttestSign(ctx, other.signArgs()); err != nil {
		t.Fatal(err)
	}
	if err := runReadinessAttestConsume(ctx, append(other.targetArgs(), "--attest", other.attestPath, "--request-id", "other")); err != nil {
		t.Fatal(err)
	}
	otherRecord, err := os.ReadFile(filepath.Join(other.root, "upgrade_readiness_consumed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "upgrade_readiness_consumed.json"), otherRecord, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(attemptPath)
	if err := verifyReleaseConsume(ctx, f.root, f.lockFD); err == nil || !strings.Contains(err.Error(), "wrong round") {
		t.Fatalf("foreign-round consume accepted: %v", err)
	}
	if err := verifyReleaseConsume(ctx, f.root, -1); err == nil || !strings.Contains(err.Error(), "inherited") {
		t.Fatalf("missing lock accepted: %v", err)
	}
}

func mustHoldSHA(t *testing.T, root string) string {
	t.Helper()
	hold, _, err := maintenance.ReadUpgradeFenceRaw(root)
	if err != nil {
		t.Fatal(err)
	}
	return sha256Hex(hold)
}

// TestReleaseAuditOrderingRecovery covers the F-AUDIT gap: a crash after the
// durable completed audit but before mark is recovered from the audit; missing
// audit is never a successful release; an audit failure can discard the
// unproven attempt.
func TestReleaseAuditOrderingRecovery(t *testing.T) {
	f := newAttestFixture(t)
	ctx := context.Background()
	if err := runReadinessAttestSign(ctx, f.signArgs()); err != nil {
		t.Fatal(err)
	}
	if err := runReadinessAttestConsume(ctx, append(f.targetArgs(), "--attest", f.attestPath, "--request-id", "audit-crash")); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseConsume(ctx, f.root, f.lockFD); err != nil {
		t.Fatal(err)
	}
	attemptPath := filepath.Join(f.root, "upgrade_release_attempt.json")
	holdSHA := mustHoldSHA(t, f.root)

	// No hold with only an attempted claim: never a success.
	if err := os.Rename(filepath.Join(f.root, "upgrade.hold"), filepath.Join(f.root, "hold.away")); err != nil {
		t.Fatal(err)
	}
	if already, err := noHoldReleaseOutcome(f.root, true); err == nil || already {
		t.Fatalf("attempt without audit claimed success: already=%t err=%v", already, err)
	}
	if already, err := noHoldReleaseOutcome(f.root, false); err == nil || already {
		t.Fatalf("legacy no-hold without audit claimed success: already=%t err=%v", already, err)
	}
	// Crash after durable audit but before mark: recover, then read back both.
	if err := maintenance.AppendUpgradeAudit(f.root, maintenance.UpgradeAuditEntry{Action: "release-completed", HoldSHA256: holdSHA, Generation: 6}); err != nil {
		t.Fatal(err)
	}
	already, err := noHoldReleaseOutcome(f.root, true)
	if err != nil || !already {
		t.Fatalf("audit-backed recovery failed: already=%t err=%v", already, err)
	}
	raw, err := os.ReadFile(attemptPath)
	if err != nil {
		t.Fatal(err)
	}
	var attempt readinessReleaseAttempt
	if err := json.Unmarshal(raw, &attempt); err != nil || attempt.State != "completed" {
		t.Fatalf("attempt not marked completed from audit: %+v %v", attempt, err)
	}
	entries, err := maintenance.ReadUpgradeAuditTail(f.root, 5)
	if err != nil || len(entries) == 0 || entries[len(entries)-1].Action != "release-completed" {
		t.Fatalf("completed audit not readable back: %+v %v", entries, err)
	}
	if already, err := noHoldReleaseOutcome(f.root, false); err != nil || !already {
		t.Fatalf("legacy completed audit not recognized: already=%t err=%v", already, err)
	}

	// Audit failure path discards the unproven attempt for a fresh round.
	other := newAttestFixture(t)
	if err := runReadinessAttestSign(ctx, other.signArgs()); err != nil {
		t.Fatal(err)
	}
	if err := runReadinessAttestConsume(ctx, append(other.targetArgs(), "--attest", other.attestPath, "--request-id", "audit-fail")); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseConsume(ctx, other.root, other.lockFD); err != nil {
		t.Fatal(err)
	}
	if err := discardReleaseAttemptOnAuditFailure(other.root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other.root, "upgrade_release_attempt.json")); !os.IsNotExist(err) {
		t.Fatalf("unproven attempt not discarded: %v", err)
	}
}
