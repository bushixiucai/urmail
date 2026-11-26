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
		"  @dashboard path /dashboard /dashboard/*",
		"  handle_path @dashboard /dashboard/* {",
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
