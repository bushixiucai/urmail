package mail_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/mail"
)

func TestPasswordHashingAndVerification(t *testing.T) {
	pw := "Secret123!@#"
	hash, err := mail.HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	if !strings.HasPrefix(hash, "{SHA512-CRYPT}$6$") {
		t.Fatalf("hash %q does not have expected prefix", hash)
	}

	if !mail.VerifyPassword(pw, hash) {
		t.Error("VerifyPassword returned false for correct password")
	}

	if mail.VerifyPassword("WrongPassword", hash) {
		t.Error("VerifyPassword returned true for wrong password")
	}
}

func TestDKIMKeypairGeneration(t *testing.T) {
	kp, err := mail.GenerateDKIMKeypair("mail")
	if err != nil {
		t.Fatalf("GenerateDKIMKeypair failed: %v", err)
	}

	if kp.Selector != "mail" {
		t.Errorf("expected selector 'mail', got %q", kp.Selector)
	}
	if !strings.HasPrefix(kp.PrivateKey, "-----BEGIN RSA PRIVATE KEY-----") {
		t.Errorf("invalid private key PEM")
	}
	if !strings.HasPrefix(kp.DNSTXT, "v=DKIM1; k=rsa; p=") {
		t.Errorf("invalid DNSTXT format: %s", kp.DNSTXT)
	}
}

func TestValidateAliases(t *testing.T) {
	mailboxes := []string{"admin@example.com", "user@example.com"}

	t.Run("valid_aliases_and_catchall", func(t *testing.T) {
		rules := []mail.AliasRule{
			{Domain: "example.com", LocalPart: "info", Target: "admin@example.com", Kind: "alias"},
			{Domain: "example.com", LocalPart: "", Target: "user@example.com", Kind: "catchall"},
		}
		if err := mail.ValidateAliases(rules, mailboxes); err != nil {
			t.Errorf("expected valid aliases, got %v", err)
		}
	})

	t.Run("conflict_with_mailbox", func(t *testing.T) {
		rules := []mail.AliasRule{
			{Domain: "example.com", LocalPart: "admin", Target: "other@example.com", Kind: "alias"},
		}
		if err := mail.ValidateAliases(rules, mailboxes); err == nil {
			t.Error("expected conflict error, got nil")
		}
	})

	t.Run("cycle_detection", func(t *testing.T) {
		rules := []mail.AliasRule{
			{Domain: "example.com", LocalPart: "a", Target: "b@example.com", Kind: "alias"},
			{Domain: "example.com", LocalPart: "b", Target: "c@example.com", Kind: "alias"},
			{Domain: "example.com", LocalPart: "c", Target: "a@example.com", Kind: "alias"},
		}
		if err := mail.ValidateAliases(rules, mailboxes); err == nil {
			t.Error("expected cycle error, got nil")
		}
	})
}
