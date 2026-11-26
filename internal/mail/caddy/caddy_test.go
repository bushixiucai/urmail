package caddy

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/nginx"
)

// TestGrafanaSiteBlockRendered pins the GRAFANA-DESIGN §5 Caddy contract:
// with a Grafana hostname the compose Caddyfile gains the monitoring site
// with the fail-closed forward_auth session check; without one the
// historical Caddyfile is unchanged.
func TestGrafanaSiteBlockRendered(t *testing.T) {
	snapshot := nginx.Snapshot{
		Domains:         []nginx.DomainConfig{{FQDN: "mail.example.test", TLSCertFile: "/certs/mail.cert", TLSKeyFile: "/certs/mail.key"}},
		GrafanaHostname: "grafana.example.test",
	}
	files, err := Generate(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	out := files[FileName]
	for _, want := range []string{
		"grafana.example.test {",
		"forward_auth 127.0.0.1:8080 {",
		"    uri      /api/v1/auth/check",
		"    copy_headers X-URMAIL-Mailbox>X-WEBAUTH-USER X-WEBAUTH-ROLE",
		"  }",
		"  reverse_proxy 127.0.0.1:3000",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("grafana block missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "forward_auth") != 1 {
		t.Fatalf("unexpected extra forward_auth blocks:\n%s", out)
	}

	// An explicit proxy target (option A compose bridge dial) replaces the
	// host loopback default.
	snapshot.AuthProxyTarget = "urmaild:8080"
	files, err = Generate(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files[FileName], "forward_auth urmaild:8080 {") {
		t.Fatalf("custom auth proxy target lost:\n%s", files[FileName])
	}

	// Without a hostname the monitoring block is absent entirely.
	files, err = Generate(nginx.Snapshot{Domains: snapshot.Domains})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(files[FileName], "grafana") || strings.Contains(files[FileName], "forward_auth") {
		t.Fatalf("monitoring block rendered without a hostname:\n%s", files[FileName])
	}
}
