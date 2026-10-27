package dovecot_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/dovecot"
)

// TestSpamSieveGlobalBeforeScript pins the MAIL-002 closed-loop contract
// between the milter header and the delivery-side foldering: exactly
// X-UrMail-Spam: yes is filed to Junk, everything else (including the
// missing header of a degraded milter) reaches the user's own scripts.
func TestSpamSieveGlobalBeforeScript(t *testing.T) {
	node := defaultNodeConfig()
	files, err := dovecot.Generate(node)
	if err != nil {
		t.Fatal(err)
	}
	script, ok := files[dovecot.FileSpamSieve]
	if !ok {
		t.Fatal("spam-before.sieve missing from Generate output")
	}
	for _, required := range []string{
		`require ["fileinto", "mailbox", "envelope", "variables", "relational", "comparator-i;ascii-numeric"];`,
		`header :is "X-UrMail-Spam" "yes"`,
		`fileinto :create "Junk";`,
		"stop;",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("spam sieve missing %q", required)
		}
	}
	// A before-script must never discard: the worst outcome is Junk foldering.
	if strings.Contains(script, "discard") {
		t.Fatal("global spam sieve must not discard mail")
	}
	conf := files[dovecot.FileDovecotConf]
	if !strings.Contains(conf, "sieve_before = /etc/dovecot/urmail-spam/spam-before.sieve") {
		t.Fatal("dovecot.conf does not reference the global spam before-script")
	}
	// The before-script must sit inside the plugin block together with the
	// user script location so both managed and legacy sieve values coexist.
	if !strings.Contains(conf, "sieve = file:/var/mail/sieve/%u.sieve") {
		t.Fatal("global sieve location missing")
	}
}

// TestSpamSieveWithLinuxSieveCompiler is opt-in like the structured-rule
// compiler check: it compiles the generated global script with dovecot's
// sievec when URMAIL_SIEVE_TEST_IMAGE is configured.
func TestSpamSieveWithLinuxSieveCompiler(t *testing.T) {
	image := os.Getenv("URMAIL_SIEVE_TEST_IMAGE")
	if image == "" {
		t.Skip("URMAIL_SIEVE_TEST_IMAGE is not configured")
	}
	files, err := dovecot.Generate(defaultNodeConfig())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "spam-before.sieve")
	if err = os.WriteFile(script, []byte(files[dovecot.FileSpamSieve]), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "dovecot.conf"), []byte("mail_uid = 65534\nmail_gid = 65534\nmail_home = /tmp\nmail_location = maildir:/tmp/test-mail\nfirst_valid_uid = 1\nlog_path = /dev/stderr\n"), 0644); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "docker", "run", "--rm", "--network", "none", "--entrypoint", "/usr/bin/sievec", "-v", dir+":/test:ro", image, "/test/spam-before.sieve", "/tmp/spam-before.svbin")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sievec rejected the global spam before-script: %v\n%s", err, output)
	}
}
