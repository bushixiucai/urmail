package validation_test

import (
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

// TestIsValidEmailCanonicalGrammar pins the canonical mailbox/alias-target
// grammar shared by the API validators and the Postfix generator. The glob
// payloads come from audit finding MAIL-01: they used to pass the API-side
// alias validator and then fail the whole-platform configuration render.
func TestIsValidEmailCanonicalGrammar(t *testing.T) {
	valid := []string{
		"admin@example.com",
		"first_last+tag@example.com",
		"user.name@sub.example.com",
	}
	for _, addr := range valid {
		if !validation.IsValidEmail(addr) {
			t.Errorf("IsValidEmail(%q) = false; want true", addr)
		}
	}

	longLocal := ""
	for len(longLocal) < 65 {
		longLocal += "a"
	}
	invalid := []string{
		"a*b@outside.test",
		"a?b@outside.test",
		"a/b@outside.test",
		"a\\b@outside.test",
		"",
		"no-at-sign",
		"a@b@c@example.com",
		"@example.com",
		longLocal + "@example.com",
		"user@example.com.",
		"user@-bad.example.com",
	}
	for _, addr := range invalid {
		if validation.IsValidEmail(addr) {
			t.Errorf("IsValidEmail(%q) = true; want false", addr)
		}
	}
}
