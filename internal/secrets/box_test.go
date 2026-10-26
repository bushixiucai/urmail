package secrets_test

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/secrets"
)

func TestLoadRaw32ByteKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	enc, err := box.EncryptString("secret")
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.DecryptString(enc)
	if err != nil || got != "secret" {
		t.Fatalf("roundtrip = %q, %v", got, err)
	}
}

func TestRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := box.EncryptString("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "enc:v1:") || strings.Contains(enc, "JBSWY3DP") {
		t.Fatalf("secret was not encrypted: %s", enc)
	}
	got, err := box.DecryptString(enc)
	if err != nil || got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("decrypt = %q, %v", got, err)
	}
}

func TestLegacyPlaintextPassesThrough(t *testing.T) {
	key := make([]byte, 32)
	box, _ := secrets.New(key)
	got, err := box.DecryptString("JBSWY3DPEHPK3PXP")
	if err != nil || got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("legacy = %q, %v", got, err)
	}
}
