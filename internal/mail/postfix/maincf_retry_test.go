package postfix_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

// TestRenderMainCFSMTPRetryLimits pins the outbound retry limits (v0.6.24):
// an unreachable remote MX must be retried from at most two addresses on the
// 900s/7200s backoff grid with a 15s connect timeout, over IPv4 only. The
// maximal_queue_lifetime must stay on the Postfix default so transient
// failures still recover; this generator must not emit it.
func TestRenderMainCFSMTPRetryLimits(t *testing.T) {
	out, err := postfix.Generate(defaultNodeConfig(), postfix.Snapshot{
		Domains: []postfix.Domain{{FQDN: "example.com", Status: "active"}},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	cfg, ok := out[postfix.FileMainCF]
	if !ok {
		t.Fatal("Generate output missing main.cf")
	}

	for _, want := range []string{
		"minimal_backoff_time = 900s",
		"maximal_backoff_time = 7200s",
		"queue_run_delay = 900s",
		"smtp_connect_timeout = 15s",
		"smtp_mx_address_limit = 2",
		"inet_protocols = ipv4",
	} {
		if !strings.Contains(cfg, want+"\n") {
			t.Fatalf("generated main.cf missing %q", want)
		}
	}
	if strings.Contains(cfg, "inet_protocols = all\n") {
		t.Fatal("generated main.cf still contains inet_protocols = all")
	}
	if strings.Contains(cfg, "maximal_queue_lifetime") {
		t.Fatal("generated main.cf must not emit maximal_queue_lifetime (keep the Postfix 5-day default)")
	}
}
