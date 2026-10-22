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

	// Format: {SHA512-CRYPT}$6$<salt>$<86-char body>
	if !strings.HasPrefix(hash, "{SHA512-CRYPT}$6$") {
		t.Fatalf("hash %q does not have expected prefix", hash)
	}
	parts := strings.Split(strings.TrimPrefix(hash, "{SHA512-CRYPT}"), "$")
	if len(parts) != 4 {
		t.Fatalf("hash %q must have exactly 3 '$'-separated parts", hash)
	}
	if body := parts[3]; len(body) != 86 {
		t.Errorf("crypt body length = %d, want 86", len(body))
	}
	if !validCryptSalt(parts[2]) {
		t.Errorf("salt %q contains characters outside the crypt alphabet", parts[2])
	}

	if !mail.VerifyPassword(pw, hash) {
		t.Error("VerifyPassword returned false for correct password")
	}

	if mail.VerifyPassword("WrongPassword", hash) {
		t.Error("VerifyPassword returned true for wrong password")
	}

	// Same password, different salt must produce a different hash.
	hash2, err := mail.HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword (2nd) failed: %v", err)
	}
	if hash2 == hash {
		t.Error("two hashes of the same password are identical: salt is not random")
	}
	if !mail.VerifyPassword(pw, hash2) {
		t.Error("VerifyPassword returned false for correct password (2nd hash)")
	}
}

func validCryptSalt(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", c) {
			return false
		}
	}
	return true
}

// TestPasswordHashKnownVectors pins HashPassword/VerifyPassword against
// reference values generated with an independent implementation:
//
//	openssl passwd -6 -salt saltstring 'Hello world!'
//	openssl passwd -6 -salt saltsalt password
//	openssl passwd -6 -salt saltstring -5 ... is irrelevant here; rounds via:
//	openssl passwd -6 -salt saltsalt -rounds 10000 password
//
// These match the official SHA-crypt spec vectors and glibc/doveadm output.
func TestPasswordHashKnownVectors(t *testing.T) {
	vectors := []struct {
		password, hash string
	}{
		{"Hello world!", "{SHA512-CRYPT}$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"},
		{"password", "{SHA512-CRYPT}$6$saltsalt$qFmFH.bQmmtXzyBY0s9v7Oicd2z4XSIecDzlB5KiA2/jctKu9YterLp8wwnSq.qc.eoxqOmSuNp2xS0ktL3nh/"},
		{"Hello world!", "{SHA512-CRYPT}$6$rounds=10000$saltstringsaltst$OW1/O6BYHV6BcXZu8QVeXbDWra3Oeqh0sbHbbMCVNSnCM/UrjmM0Dp8vOuZeHBy/YTBmSK6H9qs/y3RnOaw5v."},
	}
	for _, v := range vectors {
		if !mail.VerifyPassword(v.password, v.hash) {
			t.Errorf("VerifyPassword(%q, %q) = false, want true (known vector)", v.password, v.hash)
		}
		if mail.VerifyPassword(v.password+"x", v.hash) {
			t.Errorf("VerifyPassword accepted wrong password for %q", v.hash)
		}
	}
}

func TestVerifyPasswordRejectsInvalidHashes(t *testing.T) {
	bad := []string{
		"",                                        // empty
		"$6$saltsalt$xyz",                         // missing Dovecot scheme prefix
		"{SHA512-CRYPT}",                          // prefix only
		"{SHA512-CRYPT}$6$saltsalt$",              // no body
		"{SHA512-CRYPT}$6$saltsalt$short",         // truncated body
		"{SHA512-CRYPT}$6$saltsalt$" + strings.Repeat("A", 86) + "extra", // oversized body
		"{SHA512-CRYPT}$5$saltsalt$" + strings.Repeat("A", 86),           // wrong scheme (SHA256-crypt)
		"{MD5}$1$saltsalt$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",     // other scheme
		"{SHA512-CRYPT}$6$rounds=abc$saltsalt$" + strings.Repeat("A", 86), // malformed rounds
		"{SHA512-CRYPT}$6$rounds=1$saltsalt$" + strings.Repeat("A", 86),   // rounds below minimum
		"{SHA512-CRYPT}$6$$" + strings.Repeat("A", 86),                    // empty salt
	}
	for _, h := range bad {
		if mail.VerifyPassword("password", h) {
			t.Errorf("VerifyPassword returned true for malformed hash %q", h)
		}
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
