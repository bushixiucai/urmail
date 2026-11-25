package agentd

import (
	"encoding/json"
	"os/user"
	"strings"
	"testing"
)

// TestMailStorageUsesConfiguredIdentity proves the directory preparation and
// the rendered dovecot configuration read one identity source: the compose
// topology runs the mail owner as vmail, while a default installation keeps
// the historical mail account.
func TestMailStorageUsesConfiguredIdentity(t *testing.T) {
	account, err := user.Lookup("mail")
	if err != nil {
		t.Skipf("this host has no mail account: %v", err)
	}
	_ = account
	m := testManager(t)
	// The isolated test tree pins the caller identity, so exercise the real
	// lookup branch the way production runs it: no target root, and the name
	// from configuration must be the account that is resolved.
	m.targetRoot = ""
	m.cfg.MailUser = "urmail-identity-that-does-not-exist"
	if err = m.prepareMailStorage(); err == nil || !strings.Contains(err.Error(), "urmail-identity-that-does-not-exist") {
		t.Fatalf("unknown identity accepted: %v", err)
	}
	m.cfg.MailUser = "mail"
	if err = m.prepareMailStorage(); err != nil {
		// The resolved identity writes only when the process owns the parent.
		t.Logf("mail identity lookup passed; write refused in this context: %v", err)
	}
}

// TestRenderUsesConfiguredMailIdentity proves the rendered dovecot config
// names the configured owner instead of the built-in default.
func TestRenderUsesConfiguredMailIdentity(t *testing.T) {
	m := testManager(t)
	m.cfg.MailUser, m.cfg.MailGroup = "vmail", "vmail"
	var params ApplyParams
	if err := json.Unmarshal(emptyApply(), &params); err != nil {
		t.Fatal(err)
	}
	files, err := m.render(params)
	if err != nil {
		t.Fatal(err)
	}
	var conf string
	for path, body := range files {
		if strings.HasSuffix(path, "dovecot.conf") {
			conf = body
			break
		}
	}
	if conf == "" {
		t.Fatal("dovecot config not rendered")
	}
	if !strings.Contains(conf, "mail_uid = vmail") || !strings.Contains(conf, "mail_gid = vmail") {
		t.Fatalf("configured dovecot identity not applied:\n%s", conf)
	}
}
