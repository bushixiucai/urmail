package dovecot_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/dovecot"
)

func recipientPolicies() []dovecot.SpamPolicy {
	return []dovecot.SpamPolicy{
		{MailboxAddress: "alice@tenant-a.test", Scope: "mailbox", SenderAddress: "news@vendor.test", List: "deny"},
		{MailboxAddress: "alice@tenant-a.test", Scope: "mailbox", SenderAddress: "news@vendor.test", List: "allow"},
		{MailboxAddress: "bob@tenant-a.test", Scope: "mailbox", SenderAddress: "news@vendor.test", List: "allow"},
		{MailboxAddress: "bob@tenant-a.test", Scope: "tenant", SenderDomain: "vendor.test", List: "deny"},
	}
}

func TestSpamPolicyValidationAndDeterminism(t *testing.T) {
	node := defaultNodeConfig()
	node.SpamPolicies = recipientPolicies()
	files, err := dovecot.Generate(node)
	if err != nil {
		t.Fatal(err)
	}
	node.SpamPolicies[0], node.SpamPolicies[2] = node.SpamPolicies[2], node.SpamPolicies[0]
	reordered, err := dovecot.Generate(node)
	if err != nil {
		t.Fatal(err)
	}
	if files[dovecot.FileSpamSieve] != reordered[dovecot.FileSpamSieve] {
		t.Fatal("policy order changed canonical generation")
	}
	node.SpamPolicies[0].MailboxAddress = "alice@tenant-a.test\"; discard; #"
	if _, err = dovecot.Generate(node); err == nil {
		t.Fatal("unsafe recipient entered global sieve")
	}
}

// TestSpamPoliciesWithRealDovecot executes the exact generated before-script for
// conflicting policies in one message's LMTP recipients. It proves both order
// independence and that an allow rule bypasses only shared classification.
func TestSpamPoliciesWithRealDovecot(t *testing.T) {
	image := os.Getenv("URMAIL_SIEVE_TEST_IMAGE")
	if image == "" {
		t.Skip("URMAIL_SIEVE_TEST_IMAGE is not configured")
	}
	node := defaultNodeConfig()
	node.SpamPolicies = recipientPolicies()
	files, err := dovecot.Generate(node)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// sieve-test exercises one script, so the fixture chains the exact
	// generated before and after scripts the way Dovecot does (before, then
	// after, sharing the flag state). The outbound gate helper is stubbed to
	// allow: the fixture has no gate socket, and the real helper contract is
	// covered by the sievegate unit tests and the generated-content checks.
	if err = os.WriteFile(filepath.Join(dir, "policy.sieve"), []byte(chainSieveScripts(files)), 0640); err != nil {
		t.Fatal(err)
	}
	permissionCheck := exec.CommandContext(t.Context(), "docker", "run", "--rm", "--network", "none", "-v", dir+":/test:ro", image, "sh", "-ec", "install -d -m 0750 -o root -g mail /etc/dovecot/urmail-spam; install -m 0640 -o root -g mail /test/policy.sieve /etc/dovecot/urmail-spam/spam-before.sieve; runuser -u mail -- test -r /etc/dovecot/urmail-spam/spam-before.sieve; ! runuser -u nobody -- test -r /etc/dovecot/urmail-spam/spam-before.sieve")
	if out, err := permissionCheck.CombinedOutput(); err != nil {
		t.Fatalf("private policy permissions: %v\n%s", err, out)
	}
	conf := sieveRuntimePluginConf
	if err = os.WriteFile(filepath.Join(dir, "dovecot.conf"), []byte(conf), 0644); err != nil {
		t.Fatal(err)
	}
	for _, shared := range []string{"yes", "no"} {
		msg := fmt.Sprintf("From: News <news@vendor.test>\r\nTo: alice@tenant-a.test, bob@tenant-a.test, carol@tenant-b.test\r\nSubject: ordinary\r\nX-UrMail-Spam: %s\r\n\r\nbody\r\n", shared)
		if err = os.WriteFile(filepath.Join(dir, "message.eml"), []byte(msg), 0644); err != nil {
			t.Fatal(err)
		}
		for _, recipient := range []string{"alice@tenant-a.test", "bob@tenant-a.test", "carol@tenant-b.test", "carol@tenant-b.test", "bob@tenant-a.test", "alice@tenant-a.test"} {
			command := exec.CommandContext(t.Context(), "docker", "run", "--rm", "--name", fmt.Sprintf("urmail-mailfix-sieve-%d", time.Now().UnixNano()), "--network", "none", "-v", dir+":/test:ro", image, "sh", "-ec", privateSieveFixture, "sieve-test", recipient)
			out, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("sieve-test %s: %v\n%s", recipient, err, out)
			}
			junk := strings.Contains(string(out), "store message in folder: Junk")
			want := recipient == "alice@tenant-a.test" || (recipient == "carol@tenant-b.test" && shared == "yes")
			if junk != want {
				t.Fatalf("recipient=%s shared=%s junk=%v want=%v\n%s", recipient, shared, junk, want, out)
			}
		}
	}
}

func TestAmbiguousFromCannotSelectPrivateAllow(t *testing.T) {
	image := os.Getenv("URMAIL_SIEVE_TEST_IMAGE")
	if image == "" {
		t.Skip("URMAIL_SIEVE_TEST_IMAGE is not configured")
	}
	node := defaultNodeConfig()
	node.SpamPolicies = recipientPolicies()
	files, err := dovecot.Generate(node)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, content := range map[string]string{
		"policy.sieve": chainSieveScripts(files),
		"dovecot.conf": sieveRuntimePluginConf,
		"message.eml":  "From: news@vendor.test\r\nFrom: attacker@evil.test\r\nSubject: ordinary\r\nX-UrMail-Spam: yes\r\n\r\nbody\r\n",
	} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(content), 0640); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(t.Context(), "docker", "run", "--rm", "--network", "none", "-v", dir+":/test:ro", image, "sh", "-ec", privateSieveFixture, "sieve-test", "bob@tenant-a.test")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "store message in folder: Junk") {
		t.Fatalf("ambiguous From bypassed shared classifier: %v\n%s", err, out)
	}
}

// chainSieveScripts concatenates the generated before and after scripts the
// way Dovecot chains them. Sieve only allows require commands at the very top
// of a script, so every require line is hoisted above both script bodies.
func chainSieveScripts(files map[string]string) string {
	var requires, body strings.Builder
	for _, script := range []string{files[dovecot.FileSpamSieve], files[dovecot.FileSpamAfterSieve]} {
		for _, line := range strings.Split(script, "\n") {
			if strings.HasPrefix(line, "require ") {
				requires.WriteString(line + "\n")
			} else {
				body.WriteString(line + "\n")
			}
		}
	}
	return requires.String() + body.String()
}

// sieveRuntimePluginConf mirrors the managed plugin contract from render.go:
// vnd.dovecot.execute stays global-only with the helper binary directory
// configured, so the outbound gate program is resolvable at runtime.
const sieveRuntimePluginConf = "mail_uid = 8\nmail_gid = 8\nmail_home = /tmp\nmail_location = maildir:/tmp/test-mail\nfirst_valid_uid = 1\nlog_path = /dev/stderr\nplugin {\n sieve_plugins = sieve_extprograms\n sieve_extensions = -vnd.dovecot.execute\n sieve_global_extensions = +vnd.dovecot.execute\n sieve_execute_bin_dir = /etc/dovecot/urmail-spam/execute\n}\n"

// The host temp tree intentionally stays private (CI uid1001, mode0700/0640).
// Root installs a runtime-owned copy; no bind-mount chmod/chown weakens it.
// The gate helper is a stub that reports "outbound allowed" so the combined
// before/after script reaches the Junk fileinto; the real helper behavior is
// covered by the sievegate unit tests.
const privateSieveFixture = `install -d -m 0755 /tmp/urmail-mailfix-input
install -m 0644 /test/dovecot.conf /tmp/urmail-mailfix-input/dovecot.conf
install -m 0644 /test/message.eml /tmp/urmail-mailfix-input/message.eml
install -d -m 0750 -o root -g mail /etc/dovecot/urmail-spam
install -d -m 0755 -o root -g mail /etc/dovecot/urmail-spam/execute
printf '#!/bin/sh\nexit 0\n' > /etc/dovecot/urmail-spam/execute/outbound-check
chmod 0755 /etc/dovecot/urmail-spam/execute/outbound-check
install -m 0640 -o root -g mail /test/policy.sieve /etc/dovecot/urmail-spam/spam-before.sieve
/usr/bin/sievec -c /tmp/urmail-mailfix-input/dovecot.conf /etc/dovecot/urmail-spam/spam-before.sieve /etc/dovecot/urmail-spam/spam-before.svbin
chown root:mail /etc/dovecot/urmail-spam/spam-before.svbin
chmod 0640 /etc/dovecot/urmail-spam/spam-before.svbin
exec /usr/bin/sieve-test -c /tmp/urmail-mailfix-input/dovecot.conf -r "$1" -f news@vendor.test /etc/dovecot/urmail-spam/spam-before.sieve /tmp/urmail-mailfix-input/message.eml
`
