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
		DovecotNode: dovecot.NodeConfig{Hostname: "mail.example.test", PrimaryDomain: "example.test", StorageRoot: "/var/mail", TLSCertFile: "/cert", TLSKeyFile: "/key", AuthDBPath: "/var/lib/urmail/dovecot-auth.db", AuthEngine: "sqlite"},
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
		"/srv/urmail/data/config/dovecot/dovecot.conf",
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
	for path := range files {
		if strings.HasPrefix(path, "/etc/") {
			t.Fatalf("injected render root emitted host /etc artifact: %s", path)
		}
		if !strings.HasPrefix(path, "/srv/urmail/data/config/postfix/") &&
			!strings.HasPrefix(path, "/srv/urmail/data/config/dovecot/") &&
			path != "/srv/urmail/data/config/caddy/Caddyfile" {
			t.Fatalf("unexpected artifact under injected render root: %s", path)
		}
	}
	conf := files["/srv/urmail/data/config/dovecot/dovecot.conf"]
	root := "/srv/urmail/data/config/dovecot"
	for _, want := range []string{
		"sieve_before = " + root + "/urmail-spam/spam-before.sieve",
		"sieve_after = " + root + "/urmail-spam/spam-after.sieve",
		"sieve_execute_bin_dir = " + root + "/urmail-spam/execute",
		"!include " + root + "/conf.d/urmail/dovecot-sql.conf.ext",
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("injected dovecot.conf missing %q:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "/etc/dovecot") {
		t.Fatalf("injected dovecot.conf still names the host tree:\n%s", conf)
	}
	sql := files["/srv/urmail/data/config/dovecot/conf.d/urmail/dovecot-sql.conf.ext"]
	args := "args = " + root + "/conf.d/urmail/db.conf"
	if strings.Count(sql, args) != 2 || strings.Contains(sql, "/etc/dovecot") {
		t.Fatalf("injected sql config did not follow IncludePrefix:\n%s", sql)
	}
	quota := files["/srv/urmail/data/config/dovecot/conf.d/urmail/dovecot-quota.conf.ext"]
	if !strings.Contains(quota, "acl = vfile:"+root+"/conf.d/urmail-global-acls") || strings.Contains(quota, "/etc/dovecot") {
		t.Fatalf("injected quota acl path did not follow IncludePrefix:\n%s", quota)
	}
	// User script and legacy sieve_dir stay on StorageRoot, not the config root.
	if !strings.Contains(conf, "sieve = file:/var/mail/sieve/%u.sieve") {
		t.Fatal("user sieve location left StorageRoot")
	}
}
