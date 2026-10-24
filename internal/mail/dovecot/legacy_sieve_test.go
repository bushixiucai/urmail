package dovecot_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/dovecot"
)

func TestLegacySieveLocationsArePerMailboxUserDBOverrides(t *testing.T) {
	node := defaultNodeConfig()
	node.StorageRoot = "/home/user-data"
	files, err := dovecot.Generate(node)
	if err != nil {
		t.Fatal(err)
	}
	query := files[dovecot.FileDBConf]
	for _, required := range []string{
		"CASE WHEN m.sieve_mode='legacy_v80' THEN 'file:/home/user-data/mail/sieve/%d/%n;active=/home/user-data/mail/sieve/%d/%n.sieve' ELSE 'file:/home/user-data/sieve/%u.sieve' END AS sieve",
		"THEN '/home/user-data/mail/sieve/%d/%n' ELSE NULL END AS sieve_dir",
		"THEN '/etc/dovecot/sieve-spam.sieve' ELSE NULL END AS sieve_before",
		"THEN '/home/user-data/mail/sieve/global_before' ELSE NULL END AS sieve_before2",
		"THEN '/home/user-data/mail/sieve/global_after' ELSE NULL END AS sieve_after",
		"THEN 'recipient' ELSE NULL END AS sieve_redirect_envelope_from",
		"urmail_domain_mail_ready(m.tenant_id,m.domain_id)",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("missing constrained userdb field %s", required)
		}
	}
	global := files[dovecot.FileDovecotConf]
	if strings.Contains(global, "global_after") || strings.Contains(global, "sieve_before") || !strings.Contains(global, "sieve = file:/home/user-data/sieve/%u.sieve") {
		t.Fatal("legacy hooks changed global managed-mailbox defaults")
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
