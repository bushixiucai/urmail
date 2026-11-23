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
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type attestFixture struct {
	dir, root, sqlitePath, required, probePath, attestPath, keyPath, pubPath string
	key                                                                      ed25519.PrivateKey
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
	f.key = priv
	f.keyPath = filepath.Join(f.dir, "release.key")
	f.pubPath = filepath.Join(f.dir, "release.pub")
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
	return []string{"--generation", "6", "--root-dir", f.root, "--sqlite", f.sqlitePath, "--sqlite-generation", "6", "--required-file", f.required}
}

func (f *attestFixture) signArgs() []string {
	return append(f.targetArgs(), "--probe", f.probePath, "--attest", f.attestPath, "--key", f.keyPath)
}

func (f *attestFixture) verifyArgs() []string {
	return append(f.targetArgs(), "--attest", f.attestPath, "--pubkey", f.pubPath)
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
	if err := runReadinessAttestVerify(ctx, append(f.targetArgs(), "--attest", filepath.Join(f.dir, "absent.json"), "--pubkey", f.pubPath)); err == nil {
		t.Fatal("missing attestation accepted")
	}
	// Wrong trusted anchor.
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(f.dir, "other.pub")
	if err := os.WriteFile(other, []byte(hex.EncodeToString(otherPub)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runReadinessAttestVerify(ctx, append(f.targetArgs(), "--attest", f.attestPath, "--pubkey", other)); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("wrong anchor accepted: %v", err)
	}
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
