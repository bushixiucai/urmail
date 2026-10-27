package postfix_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

// TestSpamMilterChain pins the MAIL-002 wiring: the spam verdict milter is
// the LAST entry of the MX (main.cf) smtpd_milters chain in every node
// variant, so it observes the Authentication-Results headers the DKIM/DMARC
// milters add. It never appears in non_smtpd_milters: locally injected
// operational mail must not be classified or Junk-filed.
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
			// The spam milter is last and follows the DKIM/DMARC milters.
			if !strings.HasSuffix(smtpdLine, "inet:127.0.0.1:11339") {
				t.Fatalf("spam milter not last in chain: %q", smtpdLine)
			}
			if !strings.Contains(smtpdLine, "inet:127.0.0.1:12301") && !strings.Contains(smtpdLine, "inet:127.0.0.1:8891") {
				t.Fatalf("spam milter replaced the DKIM chain: %q", smtpdLine)
			}
			// non_smtpd_milters must stay free of the spam milter.
			for _, line := range strings.Split(main, "\n") {
				if strings.HasPrefix(line, "non_smtpd_milters = ") && strings.Contains(line, "11339") {
					t.Fatal("spam milter must not classify locally injected mail")
				}
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
