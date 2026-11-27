package config

import "testing"

// TestMailIdentityDefaultsAndOverrides pins one source for the mail owner.
func TestMailIdentityDefaultsAndOverrides(t *testing.T) {
	if got := mailIdentity(""); got != "mail" {
		t.Fatalf("default identity %q", got)
	}
	if got := mailIdentity("  vmail "); got != "vmail" {
		t.Fatalf("override identity %q", got)
	}
}
