package postfix_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

// composeNode is the post-cutover topology: rendered config dir injected,
// no preserved legacy stack, policy service enabled.
func composeNode() postfix.NodeConfig {
	n := defaultNodeConfig()
	n.ConfigDir = "/srv/urmail/config/postfix"
	n.PolicyEnabled = true
	n.PolicySocket = "/srv/urmail/config/run/policy.sock"
	return n
}

// TestComposeSubmissionHasNoDanglingMilterOverride is the QA TC-06/08/09
// root-cause regression: the compose shape runs no DKIM/from-bind milters,
// so the submission/smtps per-service override (full chain +
// milter_default_action=tempfail) turned every authenticated submission
// into 454/451. The compose render must emit no override at all — the
// services inherit main.cf, whose accept default degrades to unsigned
// delivery instead of refusing mail.
func TestComposeSubmissionHasNoDanglingMilterOverride(t *testing.T) {
	out, err := postfix.Generate(composeNode(), sampleSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	master := out[postfix.FileMasterCF]
	if strings.Contains(master, "smtpd_milters=") || strings.Contains(master, "milter_default_action=") {
		t.Fatalf("compose master.cf carries a milter override:\n%s", master)
	}
	// The policy check must not appear in any master.cf shape: a -o value
	// cannot contain spaces (the space form broke the parser itself) and the
	// comma form parsed as an unknown restriction. The global main.cf check
	// covers every smtpd service instead.
	if strings.Contains(master, "check_policy_service") {
		t.Fatalf("compose master.cf carries a policy check:\n%s", master)
	}
	if !strings.Contains(master, "submission inet") || !strings.Contains(master, "smtps") {
		t.Fatalf("compose master.cf lost the submission services:\n%s", master)
	}

	// The v80 preserve shape keeps the fail-closed chain: its milters are
	// live host services (post-cutover evidence: 8891/8893 listening).
	v80 := defaultNodeConfig()
	v80.V80Preserve = true
	v80.PolicyEnabled = true
	v80.PolicySocket = "private/policy"
	out, err = postfix.Generate(v80, sampleSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out[postfix.FileMasterCF], "-o milter_default_action=tempfail") != 2 {
		t.Fatalf("v80 master.cf lost the submission/smtps fail-closed overrides:\n%s", out[postfix.FileMasterCF])
	}

	// Host shape (no config dir) keeps the historical override as well;
	// with the policy service enabled it also keeps both policy checks.
	hostEnabled := defaultNodeConfig()
	hostEnabled.PolicyEnabled = true
	hostEnabled.PolicySocket = "private/policy"
	out, err = postfix.Generate(hostEnabled, sampleSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out[postfix.FileMasterCF], "-o milter_default_action=tempfail") != 2 {
		t.Fatalf("host master.cf lost the submission/smtps overrides:\n%s", out[postfix.FileMasterCF])
	}
	for _, shape := range []struct{ name, master string }{
		{"v80", func() string {
			n := defaultNodeConfig()
			n.V80Preserve = true
			n.PolicyEnabled = true
			n.PolicySocket = "private/policy"
			out, err := postfix.Generate(n, sampleSnapshot())
			if err != nil {
				t.Fatal(err)
			}
			return out[postfix.FileMasterCF]
		}()},
		{"host", out[postfix.FileMasterCF]},
	} {
		if strings.Contains(shape.master, "smtpd_data_restrictions") {
			t.Fatalf("%s master.cf still carries a data-restrictions override:\n%s", shape.name, shape.master)
		}
	}
}

// TestComposePostfixLogsToStdout is the QA TC-12 regression: the container
// has no syslog, so the rendered main.cf must route mail logging to stdout
// (docker logs); the host deployment keeps the syslog path unchanged.
func TestComposePostfixLogsToStdout(t *testing.T) {
	out, err := postfix.Generate(composeNode(), sampleSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out[postfix.FileMainCF], "maillog_file = /dev/stdout") {
		t.Fatalf("compose main.cf missing maillog_file:\n%s", out[postfix.FileMainCF])
	}
	out, err = postfix.Generate(defaultNodeConfig(), sampleSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out[postfix.FileMainCF], "maillog_file") {
		t.Fatalf("host main.cf must keep the syslog default:\n%s", out[postfix.FileMainCF])
	}
}
