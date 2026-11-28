package caddy

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/nginx"
)

// TestDashboardRouteEmbeddedInPrimarySite pins the 2026-11-27 entry-path
// decision: the monitoring plane lives at https://<primary host>/dashboard
// inside the SAME site block (handle_path strips the prefix, serve-from-
// sub-path on the Grafana side), fail-closed forward_auth intact; the
// deprecated standalone grafana.<domain> host block is never emitted.
func TestDashboardRouteEmbeddedInPrimarySite(t *testing.T) {
	snapshot := nginx.Snapshot{
		Domains: []nginx.DomainConfig{
			{FQDN: "box.example.test", TLSCertFile: "/certs/box.cert", TLSKeyFile: "/certs/box.key"},
			{FQDN: "mail.example.test", TLSCertFile: "/certs/mail.cert", TLSKeyFile: "/certs/mail.key"},
		},
		GrafanaHostname: "box.example.test",
	}
	files, err := Generate(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	out := files[FileName]
	for _, want := range []string{
		"box.example.test {",
		"  redir /dashboard /dashboard/ 301",
		"  handle /dashboard/* {",
		"    forward_auth 127.0.0.1:8080 {",
		"      uri      /api/v1/auth/check",
		"      copy_headers X-URMAIL-Mailbox>X-WEBAUTH-USER X-WEBAUTH-ROLE",
		"    }",
		"    reverse_proxy 127.0.0.1:3000",
		"  }",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dashboard route missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "forward_auth") != 1 {
		t.Fatalf("unexpected extra forward_auth blocks:\n%s", out)
	}
	// PF-1/PF-2 regression: the prefix-keeping handle (never handle_path,
	// which cannot take a named matcher and structurally loops with
	// serve-from-sub-path) plus the 301 normalizer must be the only
	// dashboard plumbing.
	if strings.Contains(out, "handle_path") {
		t.Fatalf("handle_path must not render:\n%s", out)
	}
	// No standalone grafana site block exists.
	if strings.Contains(out, "grafana.") {
		t.Fatalf("deprecated grafana subdomain block rendered:\n%s", out)
	}
	// The mail site stays a plain proxy.
	if strings.Contains(strings.SplitN(out, "mail.example.test {", 2)[1], "dashboard") {
		t.Fatalf("dashboard route leaked into a non-primary site:\n%s", out)
	}

	// An explicit proxy target replaces the host loopback default.
	snapshot.AuthProxyTarget = "urmaild:8080"
	files, err = Generate(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files[FileName], "forward_auth urmaild:8080 {") {
		t.Fatalf("custom auth proxy target lost:\n%s", files[FileName])
	}

	// A configured host that renders no site is skipped fail-closed.
	files, err = Generate(nginx.Snapshot{Domains: snapshot.Domains, GrafanaHostname: "other.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(files[FileName], "handle_path") {
		t.Fatalf("dashboard route rendered for a host without a site:\n%s", files[FileName])
	}

	// Without a hostname the monitoring route is absent entirely.
	files, err = Generate(nginx.Snapshot{Domains: snapshot.Domains})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(files[FileName], "forward_auth") {
		t.Fatalf("monitoring route rendered without a hostname:\n%s", files[FileName])
	}
}

// TestTransportOnlyDomainPolicyOnly pins the compose mirror of the host
// nginx TransportOnly contract (pm-compose-mtasts-policy-only-1): a
// transport-only mail domain serves exactly the MTA-STS policy and refuses
// every other path — never the generic application upstream, never the
// dashboard route.
func TestTransportOnlyDomainPolicyOnly(t *testing.T) {
	snapshot := nginx.Snapshot{
		Domains: []nginx.DomainConfig{
			{FQDN: "box.example.test", TLSCertFile: "/certs/box.cert", TLSKeyFile: "/certs/box.key"},
			{FQDN: "mail.example.test", TLSCertFile: "/certs/mail.cert", TLSKeyFile: "/certs/mail.key", TransportOnly: true},
		},
		GrafanaHostname: "box.example.test",
	}
	files, err := Generate(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	out := files[FileName]

	// Extract the mail.example.test site block.
	start := strings.Index(out, "mail.example.test {")
	if start < 0 {
		t.Fatalf("transport site block missing:\n%s", out)
	}
	end := strings.Index(out[start:], "\n}\n")
	block := out[start : start+end+3]
	for _, want := range []string{
		"  tls /certs/mail.cert /certs/mail.key",
		"  handle /.well-known/mta-sts.txt {",
		"    reverse_proxy 127.0.0.1:8080",
		"  }",
		"  handle {",
		"    respond 404",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("policy block missing %q:\n%s", want, block)
		}
	}
	// No web product plumbing on the transport domain.
	for _, forbidden := range []string{
		"  reverse_proxy 127.0.0.1:8080\n}\n",
		"forward_auth",
		"/dashboard",
		"/mail/",
	} {
		if strings.Contains(block, forbidden) {
			t.Fatalf("transport block must not carry web product plumbing %q:\n%s", forbidden, block)
		}
	}

	// Normal domains do not regress: the primary site keeps its dashboard
	// route and site proxy.
	if !strings.Contains(out, "box.example.test {") || !strings.Contains(out, "reverse_proxy 127.0.0.1:8080\n}\n\nmail.example.test {") {
		t.Fatalf("normal domain regressed:\n%s", out)
	}

	// Same input renders byte-identically (repeat-render determinism).
	again, err := Generate(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if again[FileName] != out {
		t.Fatal("repeat render diverged")
	}
}
