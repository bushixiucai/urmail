package reconcile_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
	"github.com/bushixiucai/urmail/internal/reconcile"
)

func TestV80PostfixProfileSurvivesSnapshotAssembly(t *testing.T) {
	cfg := &config.Config{MyHostname: "box.example.com", PrimaryDomain: "example.com", StorageRoot: "/var/mail", PolicyEnabled: true, PolicySocket: "private/policy", PostfixV80Preserve: true, DatabaseURLDovecot: "fixture", ListenAddr: "127.0.0.1:8080"}
	assembled, err := reconcile.AssembleConfigContext(t.Context(), cfg, &mockQuerier{}, "192.0.2.1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !assembled.PostfixNode.V80Preserve {
		t.Fatal("daemon assembly dropped node compatibility profile")
	}
	files, err := postfix.Generate(assembled.PostfixNode, assembled.PostfixSnap)
	if err != nil || !strings.Contains(files[postfix.FileMainCF], "message_size_limit = 134217728") {
		t.Fatal("assembled profile lost measured SMTP limit", err)
	}
}
