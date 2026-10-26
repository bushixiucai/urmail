package panelpassword

import (
	"context"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/mail"
)

func TestPanelArgon2AndLegacyCompatibility(t *testing.T) {
	password := "correct-horse-battery"
	hash, err := Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") || !Verify(password, hash) || Verify("wrong", hash) || NeedsUpgrade(hash) {
		t.Fatal("Argon2 verifier contract")
	}
	legacy, err := mail.HashPassword("short")
	if err != nil {
		t.Fatal(err)
	}
	if !Verify("short", legacy) || !NeedsUpgrade(legacy) {
		t.Fatal("legacy verification rejected")
	}
	for _, bad := range []string{strings.Replace(hash, "m=32768", "m=999999999", 1), strings.Replace(hash, "t=2", "t=99999", 1), hash + "$extra"} {
		if Verify(password, bad) {
			t.Fatal("unbounded/malformed Argon2 accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = VerifyContext(ctx, password, hash); err == nil {
		t.Fatal("canceled verification started")
	}
}
func TestOversizedPasswordsNeverEnterHashing(t *testing.T) {
	oversized := strings.Repeat("x", 4097)
	if _, err := Hash(oversized); err == nil {
		t.Fatal("oversized hash accepted")
	}
	if Verify(oversized, "$argon2id$v=19$m=32768,t=2,p=1$bad$bad") || mail.VerifyPassword(oversized, "{SHA512-CRYPT}$6$bad$bad") {
		t.Fatal("oversized verification accepted")
	}
}

func TestHistoricalRehashDoesNotChangePasswordPolicy(t *testing.T) {
	for _, password := range []string{"short", strings.Repeat("x", 257)} {
		hash, err := Rehash(context.Background(), password)
		if err != nil || !Verify(password, hash) {
			t.Fatal("historical credential migration", err)
		}
		if _, err = Hash(password); err == nil {
			t.Fatal("historical compatibility weakened new-password policy")
		}
	}
}
