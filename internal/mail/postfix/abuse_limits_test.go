package postfix_test

import (
	"strings"
	"testing"

	postfix "github.com/bushixiucai/urmail/internal/mail/postfix"
)

// TestAbuseLimitsRenderedAndBoundariesKept covers MAIL-003 acceptance:
// per-client rate limits and error budgets are part of the generated policy
// (so they survive restarts), while the existing From-authorization, relay
// rejection and policy-service rules stay in place — a protection change must
// never cancel the durable boundaries.
func TestAbuseLimitsRenderedAndBoundariesKept(t *testing.T) {
	out, err := postfix.Generate(defaultNodeConfig(), postfix.Snapshot{
		Domains:   []postfix.Domain{{FQDN: "example.com", Status: "active"}},
		Mailboxes: []postfix.Mailbox{{Address: "alice@example.com", Status: "active"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	main, ok := out[postfix.FileMainCF]
	if !ok {
		t.Fatalf("main.cf missing from generated output: %v", out)
	}

	// anvil-enforced per-client budgets.
	for _, want := range []string{
		"smtpd_client_connection_count_limit = 20",
		"smtpd_client_connection_rate_limit = 60",
		"smtpd_client_auth_rate_limit = 15",
		"smtpd_client_message_rate_limit = 120",
		"smtpd_client_recipient_rate_limit = 240",
		"smtpd_error_sleep_time = 5s",
		"smtpd_soft_error_limit = 5",
		"smtpd_hard_error_limit = 10",
	} {
		if !strings.Contains(main, want+"\n") {
			t.Errorf("generated main.cf lacks %q", want)
		}
	}

	// Durable boundaries still present (acceptance: bans/limits never cancel
	// From-authorization and relay rejection).
	for _, want := range []string{
		"smtpd_sender_login_maps = hash:/etc/postfix/urmail/sender_login.map",
		"reject_sender_login_mismatch",
		"smtpd_relay_restrictions = permit_mynetworks, permit_sasl_authenticated, reject_unauth_destination",
		"smtpd_tls_auth_only = yes",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("generated main.cf lost durable boundary %q", want)
		}
	}

	// anvil must stay defined in master.cf, otherwise the rate limits silently
	// stop working.
	master := out[postfix.FileMasterCF]
	if master == "" {
		t.Fatalf("master.cf missing from generated output")
	}
	if !strings.Contains(master, "anvil     unix") {
		t.Errorf("master.cf no longer defines the anvil service")
	}
}

// TestAbuseLimitsDeterministic proves the policy is a pure function of the
// snapshot: the same snapshot renders byte-identical limits, which is what
// makes the protection reappear after every restart.
func TestAbuseLimitsDeterministic(t *testing.T) {
	snap := postfix.Snapshot{Domains: []postfix.Domain{{FQDN: "example.com", Status: "active"}}}
	first, err := postfix.Generate(defaultNodeConfig(), snap)
	if err != nil {
		t.Fatal(err)
	}
	second, err := postfix.Generate(defaultNodeConfig(), snap)
	if err != nil {
		t.Fatal(err)
	}
	if first[postfix.FileMainCF] != second[postfix.FileMainCF] {
		t.Fatal("main.cf is not deterministic across runs")
	}
}
