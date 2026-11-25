package agentd

import (
	"os"
	"os/user"
	"strconv"
	"testing"
)

// TestSpamSieveUsesConfiguredGroup proves the published spam sieve is owned by
// the configured mail group so a dovecot running as that identity can read it.
func TestSpamSieveUsesConfiguredGroup(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("group ownership is only applied by the privileged agent")
	}
	group, err := user.LookupGroup("mail")
	if err != nil {
		t.Skipf("this host has no mail group: %v", err)
	}
	wantGID, err := strconv.Atoi(group.Gid)
	if err != nil {
		t.Fatal(err)
	}
	m := testManager(t)
	m.cfg.MailGroup = "mail"
	const path = "/etc/dovecot/urmail-spam/spam-before.sieve"
	if err = m.write(path, []byte("require [\"fileinto\"];\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(m.target(path))
	if err != nil {
		t.Fatal(err)
	}
	_, gid := fileOwner(info)
	if gid != wantGID {
		t.Fatalf("spam sieve gid=%d, want the mail group %d", gid, wantGID)
	}
	m.cfg.MailGroup = "urmail-group-that-does-not-exist"
	if err = m.write(path, []byte("require [\"fileinto\"];\n"), 0o644); err == nil {
		t.Fatal("unknown mail group accepted")
	}
}
