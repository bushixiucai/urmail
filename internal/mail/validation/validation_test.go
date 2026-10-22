package validation_test

import (
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

func TestIsValidHostname(t *testing.T) {
	tests := []struct {
		host  string
		valid bool
	}{
		{"example.com", true},
		{"sub.example.com", true},
		{"box.urmail.cn", true},
		{"", false},
		{"-bad.com", false},
		{"bad-.com", false},
		{"inv@lid.com", false},
	}

	for _, tc := range tests {
		if got := validation.IsValidHostname(tc.host); got != tc.valid {
			t.Errorf("IsValidHostname(%q) = %v; want %v", tc.host, got, tc.valid)
		}
	}
}
