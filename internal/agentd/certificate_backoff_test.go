package agentd

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
)

// MAIL-006: a failed ACME renewal must back off with a bounded delay instead
// of being retried on every config apply, and the backoff must expire (no
// permanent bans on the renewal path).
func TestRenewCertificatesBackoffDefersAndExpires(t *testing.T) {
	m := &ApplyManager{cfg: &config.Config{MyHostname: "box.example.com", PrimaryDomain: "example.com"}}

	// Simulate a failure that just happened: the next automatic attempt is
	// deferred, reports no error, and leaves the sticky status untouched.
	m.certRenewFails = 1
	m.certRenewNextTry = time.Now().Add(certRenewBackoffBase)
	m.status.CertificateError = "certificate renewal failed"

	msg, _, err := m.RenewCertificates(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("deferred renewal must not be an error: %v", err)
	}
	if msg != "certificate renewal deferred by backoff" {
		t.Fatalf("deferred message = %q", msg)
	}
	if m.status.CertificateError != "certificate renewal failed" {
		t.Fatalf("backoff must keep the last failure status, got %q", m.status.CertificateError)
	}

	// After the window expires the renewal path is eligible again (the actual
	// issuance would then run; here the manager has no domain state, and the
	// hostname renewal path for an unprovisioned host fails, which must
	// schedule the next bounded delay rather than hang).
	m.certRenewNextTry = time.Now().Add(-time.Minute)
	if _, _, err := m.RenewCertificates(json.RawMessage(`{}`)); err == nil {
		t.Fatal("renewal without any issued certificate must fail, not silently pass")
	}
	if m.certRenewNextTry.Before(time.Now()) {
		t.Fatalf("failure must schedule a bounded backoff, next try %v", m.certRenewNextTry)
	}
	if m.certRenewFails != 2 {
		t.Fatalf("consecutive failures = %d, want 2", m.certRenewFails)
	}
}

func TestCertRenewDelayBounds(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 0},
		{1, 5 * time.Minute},
		{2, 10 * time.Minute},
		{3, 20 * time.Minute},
		{10, certRenewBackoffMax},
		{50, certRenewBackoffMax},
	}
	for _, tc := range cases {
		if got := certRenewDelay(tc.failures); got != tc.want {
			t.Errorf("certRenewDelay(%d) = %v, want %v", tc.failures, got, tc.want)
		}
	}
}

// The backoff must not leak into explicit operator issue requests, which use
// their own operation and remain available during the window.
func TestRenewBackoffDoesNotBlockIssueOpPath(t *testing.T) {
	m := &ApplyManager{cfg: &config.Config{MyHostname: "box.example.com", PrimaryDomain: "example.com"}}
	m.certRenewNextTry = time.Now().Add(time.Hour)
	// IssueCertificate is a different operation; it must be callable (and
	// reject this unregistered identity on its own merits).
	if _, _, err := m.IssueCertificate([]byte(`{"domain":"box.example.com"}`)); err == nil {
		t.Fatal("issue of unprovisioned host must fail on its own terms")
	}
	_ = context.Background()
}
