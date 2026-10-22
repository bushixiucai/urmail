package opendkim_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/opendkim"
)

const sampleKey = "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAK\n-----END RSA PRIVATE KEY-----\n"

func TestGenerateSigningTablesAndKey(t *testing.T) {
	files, err := opendkim.Generate(opendkim.NodeConfig{}, opendkim.Snapshot{Domains: []opendkim.DomainKey{
		{Domain: "Example.COM", Selector: "mail", PrivateKey: sampleKey},
	}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(files[opendkim.ConfPath], "SigningTable refile:"+opendkim.SigningTablePath) {
		t.Fatalf("missing signing table in config: %s", files[opendkim.ConfPath])
	}
	if !strings.Contains(files[opendkim.KeyTablePath], "mail._domainkey.example.com example.com:mail:/etc/opendkim/keys/example.com/mail.private") {
		t.Fatalf("unexpected key table: %s", files[opendkim.KeyTablePath])
	}
	if !strings.Contains(files[opendkim.SigningTablePath], "*@example.com mail._domainkey.example.com") {
		t.Fatalf("unexpected signing table: %s", files[opendkim.SigningTablePath])
	}
	keyPath := "/etc/opendkim/keys/example.com/mail.private"
	if files[keyPath] != sampleKey {
		t.Fatalf("key content not rendered")
	}
}

func TestGenerateRejectsInvalidInput(t *testing.T) {
	cases := []opendkim.Snapshot{
		{Domains: []opendkim.DomainKey{{Domain: "../evil", Selector: "mail", PrivateKey: sampleKey}}},
		{Domains: []opendkim.DomainKey{{Domain: "example.com", Selector: "bad selector", PrivateKey: sampleKey}}},
		{Domains: []opendkim.DomainKey{{Domain: "example.com", Selector: "mail", PrivateKey: ""}}},
		{Domains: []opendkim.DomainKey{
			{Domain: "example.com", Selector: "mail", PrivateKey: sampleKey},
			{Domain: "example.com", Selector: "mail", PrivateKey: sampleKey},
		}},
	}
	for _, c := range cases {
		if _, err := opendkim.Generate(opendkim.NodeConfig{}, c); err == nil {
			t.Fatalf("expected rejection for %+v", c)
		}
	}
}
