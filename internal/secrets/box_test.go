package secrets_test

import (
	"crypto/rand"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/secrets"
)

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
