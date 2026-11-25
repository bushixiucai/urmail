package mail_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail"
	"github.com/bushixiucai/urmail/internal/mail/dovecot"
	"github.com/bushixiucai/urmail/internal/mail/nginx"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

func baseContext() mail.ConfigContext {
	return mail.ConfigContext{
		DovecotNode: dovecot.NodeConfig{Hostname: "mail.example.test", PrimaryDomain: "example.test", StorageRoot: "/var/mail", TLSCertFile: "/cert", TLSKeyFile: "/key", AuthDBPath: "/var/lib/urmail/dovecot-auth.db"},
		NSDNode:     nsd.NodeConfig{Hostname: "mail.example.test", PrimaryDomain: "example.test", PublicIPv4: "192.0.2.1", Nameservers: []string{"ns.example.test"}},
		PostfixNode: postfix.NodeConfig{Hostname: "mail.example.test", PrimaryDomain: "example.test", StorageRoot: "/var/mail", TLSCertFile: "/cert", TLSKeyFile: "/key"},
		NginxNode:   nginx.NodeConfig{PrimaryDomain: "example.test", StorageRoot: "/var/mail", BackendHTTP: "http://127.0.0.1:8080"},
		NginxSnap:   nginx.Snapshot{Domains: []nginx.DomainConfig{{FQDN: "mail.example.test", TLSCertFile: "/cert", TLSKeyFile: "/key"}}},
	}
}

// TestRenderRootDefaultsToEtc keeps host deployments on the historical tree.
func TestRenderRootDefaultsToEtc(t *testing.T) {
	files, err := mail.RenderAll(baseContext())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for path := range files {
		if strings.HasPrefix(path, "/etc/postfix/") || strings.HasPrefix(path, "/etc/dovecot/") {
			found = true
		}
		if strings.HasPrefix(path, "/caddy/") || strings.HasPrefix(path, "caddy/") {
			t.Fatalf("caddy artifact rendered without an injected root: %s", path)
		}
	}
	if !found {
		t.Fatal("default render root did not produce /etc artifacts")
	}
}

// TestRenderRootInjectsComposeLayout pins the contract paths and the Caddyfile.
func TestRenderRootInjectsComposeLayout(t *testing.T) {
	ctx := baseContext()
	ctx.RenderRoot = "/srv/urmail/data/config"
	files, err := mail.RenderAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	required := []string{
		"/srv/urmail/data/config/postfix/main.cf",
		"/srv/urmail/data/config/dovecot/conf.d/urmail/dovecot.conf",
		"/srv/urmail/data/config/caddy/Caddyfile",
	}
	for _, path := range required {
		if _, ok := files[path]; !ok {
			t.Fatalf("missing rendered artifact %s", path)
		}
	}
	caddy := files["/srv/urmail/data/config/caddy/Caddyfile"]
	if !strings.Contains(caddy, "mail.example.test {") || !strings.Contains(caddy, "reverse_proxy 127.0.0.1:8080") {
		t.Fatalf("caddy site block incomplete:\n%s", caddy)
	}
}
