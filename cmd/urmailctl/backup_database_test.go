package main

import (
	"context"
	"strings"
	"testing"
)

func TestBackupPeerRestrictsTargetAndSecrets(t *testing.T) {
	peer, err := backupPeerDatabaseURL("postgres://urmail_migrate:private-fixture@127.0.0.1:5432/urmail?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	command, err := backupDatabaseCommand(context.Background(), peer)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(command.Args, " "), "private-fixture") || command.Path != "/usr/sbin/runuser" {
		t.Fatal("unsafe backup command")
	}
	for _, entry := range command.Env {
		if strings.HasPrefix(entry, "PG") {
			t.Fatal("ambient PostgreSQL override inherited")
		}
	}
	for _, raw := range []string{"postgres://urmail_platform:x@localhost/urmail", "postgres://urmail_migrate:x@external.test/urmail", "postgres://urmail_migrate:x@localhost/urmail?options=-crole=postgres", "postgres://urmail_migrate:x@localhost/urmail?host=/tmp/foreign", "postgres://urmail_migrate:x@localhost/urmail?dbname=other", "postgres://urmail_migrate:x@localhost/not-allowed"} {
		if _, err := backupPeerDatabaseURL(raw); err == nil {
			t.Fatal("unsafe target accepted")
		}
	}
}
