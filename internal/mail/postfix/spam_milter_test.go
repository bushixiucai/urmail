package postfix_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

// TestSpamMilterChain pins the MAIL-002 wiring after the rev-8 cleanup: the
// retired spampd port 11339 appears NOWHERE in the rendered set (it used to
// sit last in the smtpd_milters chain as the content-verdict slot; with the
// verdict service decommissioned it was a dead connect on every message),
// and the signer milter stays in every smtpd chain.
func TestSpamMilterChain(t *testing.T) {
	variants := map[string]postfix.NodeConfig{
		"standard": defaultNodeConfig(),
		"v80_signer": func() postfix.NodeConfig {
			n := defaultNodeConfig()
			n.V80Preserve = true
			n.PolicyEnabled = true
			return n
		}(),
		"v80_external_dkim": func() postfix.NodeConfig {
			n := defaultNodeConfig()
			n.V80Preserve = true
			n.PolicyEnabled = true
			n.PreserveExternalOpenDKIM = true
			return n
		}(),
	}
	for name, node := range variants {
		t.Run(name, func(t *testing.T) {
			files, err := postfix.Generate(node, postfix.Snapshot{Domains: []postfix.Domain{{FQDN: "example.com", Status: "active"}}})
			if err != nil {
				t.Fatal(err)
			}
			main := files[postfix.FileMainCF]
			for _, required := range []string{
				"milter_default_action = accept\n",
				"milter_protocol = 6\n",
				"smtpd_milters = ",
			} {
				if !strings.Contains(main, required) {
					t.Fatalf("%s: missing %q", name, required)
				}
			}
			var smtpdLine string
			for _, line := range strings.Split(main, "\n") {
				if strings.HasPrefix(line, "smtpd_milters = ") {
					smtpdLine = line
					break
				}
			}
			if smtpdLine == "" {
				t.Fatal("smtpd_milters line missing")
			}
			// The signer milter stays in the smtpd chain of every variant.
			if !strings.Contains(smtpdLine, "inet:127.0.0.1:12301") && !strings.Contains(smtpdLine, "inet:127.0.0.1:8891") {
				t.Fatalf("signer/DKIM chain missing: %q", smtpdLine)
			}
			// The retired spampd port must be gone from the whole rendered
			// set — a dead chain entry costs a refused connect per message
			// and fires spam_milter_unavailable spuriously.
			if strings.Contains(main, "11339") {
				t.Fatalf("retired spam milter port still rendered: %q", smtpdLine)
			}
			// The documented degrade path: an unavailable milter accepts.
			// milter_default_action=accept in main.cf IS the degrade policy;
			// the operational alert (spam_milter_unavailable) surfaces it.
			master := files[postfix.FileMasterCF]
			if strings.Contains(master, "11339") {
				t.Fatal("submission chain must not depend on the spam milter: its fail-closed milter_default_action=tempfail would reject user submissions during a spam-service outage")
			}
		})
	}
}
