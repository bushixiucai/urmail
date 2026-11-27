package dovecot_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/dovecot"
)

// TestDBConfFollowsTheEngine pins the compatibility split: a PostgreSQL
// deployment keeps the DSN contract, and only the single engine renders the
// snapshot path.
func TestDBConfFollowsTheEngine(t *testing.T) {
	node := defaultNodeConfig()
	files, err := dovecot.Generate(node)
	if err != nil {
		t.Fatal(err)
	}
	pg := files[dovecot.FileDBConf]
	if !strings.Contains(pg, "driver = pgsql") || !strings.Contains(pg, "connect = "+node.DatabaseURL) {
		t.Fatalf("postgres mode did not render the DSN contract:\n%s", pg)
	}
	if strings.Contains(pg, "driver = sqlite") {
		t.Fatal("postgres mode rendered the snapshot driver")
	}

	node.AuthEngine = "sqlite"
	node.AuthDBPath = "/var/lib/urmail/dovecot-auth.db"
	files, err = dovecot.Generate(node)
	if err != nil {
		t.Fatal(err)
	}
	snap := files[dovecot.FileDBConf]
	if !strings.Contains(snap, "driver = sqlite") || !strings.Contains(snap, "connect = /var/lib/urmail/dovecot-auth.db") {
		t.Fatalf("sqlite mode did not render the snapshot contract:\n%s", snap)
	}
	if strings.Contains(snap, node.DatabaseURL) {
		t.Fatal("sqlite mode leaked the PostgreSQL DSN")
	}
}
