package postfix_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

func TestV80PreserveGoldenAndPolicyOrder(t *testing.T) {
	node := defaultNodeConfig()
	node.V80Preserve = true
	node.PolicyEnabled = true
	snap := postfix.Snapshot{Domains: []postfix.Domain{{FQDN: "example.com", Status: "active"}}, Mailboxes: []postfix.Mailbox{{ID: 1, TenantID: 2, Address: "self@example.com", Status: "active"}}, Aliases: []postfix.Alias{{Domain: "example.com", LocalPart: "manual", Target: "elsewhere@example.net", Kind: "alias", SenderLogins: []string{"self@example.com"}}}}
	files, err := postfix.Generate(node, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{postfix.FileMainCF, postfix.FileMasterCF, postfix.FileSenderLoginMap} {
		path := filepath.Join("testdata", "v80_preserve", name)
		if *update {
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, []byte(files[name]), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		golden, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if files[name] != string(golden) {
			t.Fatalf("v80 golden mismatch %s", name)
		}
	}
	main := files[postfix.FileMainCF]
	sender := "smtpd_sender_restrictions =\n    reject_non_fqdn_sender\n    reject_unknown_sender_domain\n    reject_authenticated_sender_login_mismatch\n    reject_rhsbl_sender dbl.spamhaus.org=127.0.1.[2..99]\n"
	recipient := "smtpd_recipient_restrictions =\n    check_policy_service unix:private/policy\n    permit_sasl_authenticated\n    permit_mynetworks\n    reject_rbl_client zen.spamhaus.org=127.0.0.[2..11]\n    reject_unlisted_recipient\n    check_policy_service inet:127.0.0.1:12340\n"
	for _, required := range []string{sender, recipient, "message_size_limit = 134217728\n", "smtpd_relay_restrictions = permit_mynetworks, permit_sasl_authenticated, reject_unauth_destination\n", "smtpd_data_restrictions = check_policy_service unix:private/policy\n", "smtpd_tls_auth_only = yes\n"} {
		if !strings.Contains(main, required) {
			t.Fatal("reviewed SMTP restriction or mandatory SaaS gate missing")
		}
	}
	if strings.Count(files[postfix.FileMasterCF], "-o smtpd_data_restrictions=check_policy_service,unix:private/policy") != 2 {
		t.Fatal("submission/smtps lost DATA policy")
	}
	// MAIL-03: the MX listener must never offer AUTH; only submission and
	// smtps do, both under forced TLS.
	if strings.Count(files[postfix.FileMasterCF], "-o smtpd_sasl_auth_enable=no") != 1 {
		t.Fatal("MX listener AUTH disable missing")
	}
	if strings.Count(files[postfix.FileMasterCF], "smtpd_sasl_auth_enable=yes") != 2 {
		t.Fatal("submission/smtps AUTH enablement changed")
	}
	// MAIL-04: submission/smtps must carry the From-authorization milter in
	// addition to the DKIM chain.
	if !strings.Contains(files[postfix.FileMasterCF], "smtpd_milters=inet:127.0.0.1:12301, inet:127.0.0.1:8893, inet:127.0.0.1:12302") {
		t.Fatal("submission/smtps From-authorization milter missing")
	}
	if strings.Count(files[postfix.FileMasterCF], "-o milter_default_action=tempfail") != 2 {
		t.Fatal("submission/smtps fail-closed milter action missing")
	}
	if !strings.Contains(files[postfix.FileSenderLoginMap], "manual@example.com self@example.com") {
		t.Fatalf("explicit alias sender authority absent: %q", files[postfix.FileSenderLoginMap])
	}
	if strings.Contains(main, "inet:127.0.0.1:8891") {
		t.Fatal("duplicate legacy DKIM signer copied")
	}
	for _, required := range []string{"virtual_transport = lmtp:[127.0.0.1]:10025\n", "smtpd_milters = inet:127.0.0.1:12301, inet:127.0.0.1:8893\n", "non_smtpd_milters = inet:127.0.0.1:12301, inet:127.0.0.1:8893\n"} {
		if !strings.Contains(main, required) {
			t.Fatalf("source filtering chain missing: %s", required)
		}
	}
	if strings.Contains(main, "content_filter") || strings.Contains(files[postfix.FileMasterCF], "10025") || strings.Contains(files[postfix.FileMasterCF], "10026") {
		t.Fatal("filtering added an SMTP reinjection/policy bypass service")
	}
}

func TestV80PreserveExternalOpenDKIMUsesLiveMilter(t *testing.T) {
	node := defaultNodeConfig()
	node.V80Preserve = true
	node.PolicyEnabled = true
	node.PreserveExternalOpenDKIM = true
	files, err := postfix.Generate(node, postfix.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	main := files[postfix.FileMainCF]
	for _, required := range []string{
		"smtpd_milters = inet:127.0.0.1:8891, inet:127.0.0.1:8893\n",
		"non_smtpd_milters = inet:127.0.0.1:8891, inet:127.0.0.1:8893\n",
	} {
		if !strings.Contains(main, required) {
			t.Fatalf("live OpenDKIM milter missing: %s", required)
		}
	}
	if strings.Contains(main, "12301") {
		t.Fatal("preserved OpenDKIM cutover must not point Postfix at 12301")
	}
}

func TestV80PreserveRejectsDisabledPolicyAndInjection(t *testing.T) {
	node := defaultNodeConfig()
	node.V80Preserve = true
	if files, err := postfix.Generate(node, postfix.Snapshot{}); err == nil || files != nil {
		t.Fatal("preservation disabled SaaS policy")
	}
	node.PolicyEnabled = true
	for _, bad := range []string{"private/policy\npermit", "private/policy,permit", "inet:127.0.0.1:12340", "private/policy\r\nmessage_size_limit=0"} {
		node.PolicySocket = bad
		if files, err := postfix.Generate(node, postfix.Snapshot{}); err == nil || files != nil {
			t.Fatalf("policy socket injection accepted %q", bad)
		}
	}
}
