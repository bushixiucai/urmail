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
	mainCF := files["/etc/postfix/main.cf"]
	if mainCF == "" {
		t.Fatal("default render did not produce /etc/postfix/main.cf")
	}
	for _, want := range []string{
		"hash:/etc/postfix/urmail/virtual_mailbox.map",
		"hash:/etc/postfix/urmail/sender_login.map",
	} {
		if !strings.Contains(mainCF, want) {
			t.Fatalf("host layout lost %q:\n%s", want, mainCF)
		}
	}
	if strings.Contains(mainCF, "/srv/urmail") {
		t.Fatalf("default render root leaked compose paths:\n%s", mainCF)
	}
}

// TestRenderRootInjectsComposeLayout pins the contract paths and the Caddyfile.
func TestRenderRootInjectsComposeLayout(t *testing.T) {
	ctx := baseContext()
	ctx.RenderRoot = "/srv/urmail/data/config"
	ctx.GrafanaHostname = "mail.example.test"
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
	// The dashboard route embeds inside the primary site block (2026-11-27
	// entry-path decision), with the fail-closed forward_auth chain.
	caddyOut := files["/srv/urmail/data/config/caddy/Caddyfile"]
	for _, want := range []string{
		"  redir /dashboard /dashboard/ 301",
		"  handle /dashboard/* {",
		"    copy_headers X-URMAIL-Mailbox>X-WEBAUTH-USER X-WEBAUTH-ROLE",
		"    reverse_proxy 127.0.0.1:3000",
	} {
		if !strings.Contains(caddyOut, want) {
			t.Fatalf("compose Caddyfile missing dashboard route %q:\n%s", want, caddyOut)
		}
	}
	if strings.Contains(caddyOut, "handle_path") {
		t.Fatalf("compose Caddyfile must not render handle_path:\n%s", caddyOut)
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
	// Postfix file maps follow the same shape contract: main.cf is read from
	// <render root>/postfix as config_dir, so every map reference must name
	// the rendered mount surface (PM-S7 render-path contract).
	mainCF := files["/srv/urmail/data/config/postfix/main.cf"]
	for _, want := range []string{
		"hash:/srv/urmail/data/config/postfix/urmail/virtual_mailbox.map",
		"hash:/srv/urmail/data/config/postfix/urmail/virtual_alias.map",
		"hash:/srv/urmail/data/config/postfix/urmail/sender_login.map",
		"regexp:/srv/urmail/data/config/postfix/urmail/spam-header-checks",
	} {
		if !strings.Contains(mainCF, want) {
			t.Fatalf("injected main.cf missing %q:\n%s", want, mainCF)
		}
	}
	if strings.Contains(mainCF, "/etc/postfix/urmail") {
		t.Fatalf("injected main.cf still names the host map tree:\n%s", mainCF)
	}
	for _, ref := range []string{"urmail/virtual_mailbox.map", "urmail/virtual_alias.map", "urmail/sender_login.map", "urmail/spam-header-checks"} {
		if _, ok := files["/srv/urmail/data/config/postfix/"+ref]; !ok {
			t.Fatalf("referenced map %s is not rendered into the mount surface", ref)
		}
	}
}
