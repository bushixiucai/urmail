package dovecot_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/dovecot"
)

func TestLegacySieveLocationsArePerMailboxUserDBOverrides(t *testing.T) {
	node := defaultNodeConfig()
	node.StorageRoot = "/home/user-data"
	node.AuthEngine = "sqlite"
	files, err := dovecot.Generate(node)
	if err != nil {
		t.Fatal(err)
	}
	query := files[dovecot.FileDBConf]
	// Materialize the shared predicate so the string assertions below see
	// the final SQL exactly as dovecot will.
	query = strings.ReplaceAll(query, "EXISTS(SELECT 1 FROM tenants t WHERE t.tenant_id = m.tenant_id AND t.status = 'active') AND EXISTS(SELECT 1 FROM domains d WHERE d.domain_id = m.domain_id AND d.tenant_id = m.tenant_id AND d.status = 'active' AND d.mail_ready = 1)", "dovecotauth.ReadyJoin")
	for _, required := range []string{
		"CASE WHEN m.sieve_mode='legacy_v80' THEN 'file:/home/user-data/mail/sieve/%d/%n;active=/home/user-data/mail/sieve/%d/%n.sieve' ELSE 'file:/home/user-data/sieve/%u.sieve' END AS sieve",
		"THEN '/home/user-data/mail/sieve/%d/%n' ELSE NULL END AS sieve_dir",
		"THEN '/home/user-data/mail/sieve/global_before' ELSE NULL END AS sieve_before2",
		"THEN '/home/user-data/mail/sieve/global_after' ELSE NULL END AS sieve_after",
		"THEN 'recipient' ELSE NULL END AS sieve_redirect_envelope_from",
		// §5.3: in SQLite mode the readiness predicate comes from
		// dovecotauth.ReadyJoin — the single source shared with the
		// snapshot schema (cutover-blockers Blocker 1; hand-copying it
		// drifted against the snapshot columns once).
		"dovecotauth.ReadyJoin",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("missing constrained userdb field %s", required)
		}
	}
	if strings.Contains(query, "sieve-spam.sieve") || strings.Contains(query, " AS sieve_before,") {
		t.Fatal("legacy userdb bypassed recipient-scoped spam policy")
	}
	global := files[dovecot.FileDovecotConf]
	// The legacy per-mailbox hooks stay out of the global config; the only
	// global sieve_before is the MAIL-002 spam before-script.
	if strings.Contains(global, "global_after") || strings.Contains(global, "sieve_before2") || strings.Contains(global, "sieve-spam.sieve") || !strings.Contains(global, "sieve = file:/home/user-data/sieve/%u.sieve") {
		t.Fatal("legacy hooks changed global managed-mailbox defaults")
	}
	if !strings.Contains(global, "sieve_before = /etc/dovecot/urmail-spam/spam-before.sieve") {
		t.Fatal("global managed path is missing the MAIL-002 spam before-script")
	}
}

func TestV80SpampdHasExactlyOneLoopbackDovecotDestination(t *testing.T) {
	node := defaultNodeConfig()
	node.V80Preserve = true
	files, err := dovecot.Generate(node)
	if err != nil {
		t.Fatal(err)
	}
	conf := files[dovecot.FileDovecotConf]
	expected := "  inet_listener lmtp {\n    address = 127.0.0.1\n    port = 10026\n  }\n"
	if strings.Count(conf, expected) != 1 || !strings.Contains(conf, "unix_listener /var/spool/postfix/private/dovecot-lmtp") {
		t.Fatal("source LMTP loopback chain missing or replaced managed unix transport")
	}
	node.V80Preserve = false
	ordinary, err := dovecot.Generate(node)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ordinary[dovecot.FileDovecotConf], "10026") {
		t.Fatal("ordinary node unexpectedly enabled legacy listener")
	}
}
