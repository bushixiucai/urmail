package dovecot

import (
	"strings"
	"testing"
)

// TestAuthFailureDelayRendered covers the MAIL-003 protocol brute-force
// requirement on the IMAP/POP3 side: the authentication failure delay is part
// of the generated policy, so it is reinstated on every service restart.
func TestAuthFailureDelayRendered(t *testing.T) {
	conf := renderDovecotConf(NodeConfig{})
	if !strings.Contains(conf, "auth_failure_delay = 2 secs\n") {
		t.Errorf("generated dovecot.conf lacks auth_failure_delay:\n%s", conf)
	}
	// The delay must not weaken the existing TLS-only posture.
	for _, want := range []string{"disable_plaintext_auth = yes", "ssl = required", "ssl_min_protocol = TLSv1.2"} {
		if !strings.Contains(conf, want) {
			t.Errorf("generated dovecot.conf lost %q", want)
		}
	}
}
