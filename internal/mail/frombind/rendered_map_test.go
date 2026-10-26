package frombind_test

import (
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/frombind"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

func TestGeneratedSenderLoginMapAuthorizesOwnDomain(t *testing.T) {
	files, err := postfix.Generate(postfix.NodeConfig{Hostname: "box.example.test", PrimaryDomain: "example.test", StorageRoot: "/var/mail", TLSCertFile: "/cert.pem", TLSKeyFile: "/key.pem"}, postfix.Snapshot{Domains: []postfix.Domain{{FQDN: "example.test", Status: "active"}}, Mailboxes: []postfix.Mailbox{{Address: "alice@example.test", Status: "active"}}})
	if err != nil {
		t.Fatal(err)
	}
	allowed := frombind.ParseSenderLoginMap(files[postfix.FileSenderLoginMap])
	domains := frombind.AllowedFromDomains(allowed["alice@example.test"])
	if decision, _ := frombind.Decide(true, domains, []string{"Alice <alice@example.test>"}); decision != frombind.Allow {
		t.Fatalf("generated map rejected mailbox owner: %v", decision)
	}
	if decision, _ := frombind.Decide(true, domains, []string{"Other <other@outside.test>"}); decision != frombind.RejectDomain {
		t.Fatalf("foreign domain accepted: %v", decision)
	}
}
