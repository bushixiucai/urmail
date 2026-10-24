package handlers

import (
	"strings"
	"testing"
)

func TestMailboxSelfServicePasswordPolicy(t *testing.T) {
	for _, value := range []string{"short", "with\x00null", "with\nline", "with\rline", "with\ttab", string([]byte{0xff}), strings.Repeat("a", 1025), "same-old-password", strings.Repeat("中", 7), strings.Repeat("中", 1025)} {
		if validMailboxPassword("same-old-password", value) {
			t.Fatal("unsafe or unchanged password accepted")
		}
	}
	if !validMailboxPassword("old-password", " 新-password-123 ") {
		t.Fatal("valid password was trimmed or rejected")
	}
	if validMailboxPassword("", "valid-new-password") {
		t.Fatal("missing old password accepted")
	}
}

func TestMailboxPasswordUnicodeCharacterBounds(t *testing.T) {
	for _, password := range []string{strings.Repeat("中", 8), strings.Repeat("中", 1024), strings.Repeat("😀", 1024)} {
		if !validMailboxPassword("old-secret", password) {
			t.Fatal("valid Unicode character bounds rejected")
		}
	}
	if validMailboxPassword("old-secret", strings.Repeat("😀", 1025)) {
		t.Fatal("overlong Unicode secret accepted")
	}
}
