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
func TestValidateAbsolutePath(t *testing.T) {
	if err := validation.ValidateAbsolutePath("/var/mail"); err != nil {
		t.Errorf("absolute path must pass: %v", err)
	}
	for _, bad := range []string{"", "relative/path", "/var mail/x", "/var\tmail", "/var\nmail"} {
		if err := validation.ValidateAbsolutePath(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestValidateSocketPath(t *testing.T) {
	for _, good := range []string{"private/policy", "unix:/run/urmail/policy.sock", "/run/urmail/policy.sock", "private/urmail-archive"} {
		if err := validation.ValidateSocketPath(good); err != nil {
			t.Errorf("%q must pass: %v", good, err)
		}
	}
	for _, bad := range []string{"", "unix:", "private /policy", "private\npolicy", "../escape", "unix:../escape"} {
		if err := validation.ValidateSocketPath(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestValidateDatabaseURL(t *testing.T) {
	if err := validation.ValidateDatabaseURL("postgres://u:p@127.0.0.1/db"); err != nil {
		t.Errorf("valid URL must pass: %v", err)
	}
	for _, bad := range []string{"", "postgres://u\nEVIL=1", "host=db\ruser=x"} {
		if err := validation.ValidateDatabaseURL(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestIPHelpers(t *testing.T) {
	if !validation.IsValidIPv4("192.0.2.1") || validation.IsValidIPv4("2001:db8::1") {
		t.Error("IPv4 classification wrong")
	}
	if !validation.IsValidIPv6("2001:db8::1") || validation.IsValidIPv6("192.0.2.1") {
		t.Error("IPv6 classification wrong")
	}
}
