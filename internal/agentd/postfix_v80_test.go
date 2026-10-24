package agentd

import (
	"os"
	"strings"
	"testing"
)

func TestApplyV80PostfixProfileFromRootConfiguration(t *testing.T) {
	m := testManager(t)
	m.cfg.PostfixV80Preserve = true
	m.cfg.PolicyEnabled = true
	m.cfg.PolicySocket = "/run/urmail-policy/policy.sock"
	if _, _, err := m.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.target("/etc/postfix/main.cf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"message_size_limit = 134217728", "reject_rbl_client zen.spamhaus.org=127.0.0.[2..11]", "check_policy_service inet:127.0.0.1:12340", "reject_unauth_destination"} {
		if !strings.Contains(string(data), required) {
			t.Fatal("agent re-render dropped reviewed compatibility profile")
		}
	}
	// Agent input cannot select or replace the privileged node profile.
	if _, _, err = m.Apply([]byte(`{"postfix":{"V80Preserve":false,"Domains":[],"Mailboxes":[],"Aliases":[]}}`)); err == nil {
		t.Fatal("runtime request overrode node preservation profile")
	}
}
