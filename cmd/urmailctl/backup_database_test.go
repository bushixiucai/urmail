package main

import (
	"context"
	"strings"
	"testing"
)

func TestBackupPeerRestrictsTargetAndSecrets(t *testing.T) {
	t.Setenv("URMAIL_BACKUP_S3_SECRET_KEY", "root-only-offsite-secret")
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
		if strings.HasPrefix(entry, "PG") || strings.Contains(entry, "root-only-offsite-secret") {
			t.Fatal("ambient PostgreSQL override inherited")
		}
	}
	for _, raw := range []string{"postgres://urmail_platform:x@localhost/urmail", "postgres://urmail_migrate:x@external.test/urmail", "postgres://urmail_migrate:x@localhost/urmail?options=-crole=postgres", "postgres://urmail_migrate:x@localhost/urmail?host=/tmp/foreign", "postgres://urmail_migrate:x@localhost/urmail?dbname=other", "postgres://urmail_migrate:x@localhost/not-allowed"} {
		if _, err := backupPeerDatabaseURL(raw); err == nil {
			t.Fatal("unsafe target accepted")
		}
	}
}

func TestBackupManagedComposeTopologyAcceptsTheProjectServiceOnly(t *testing.T) {
	t.Setenv("URMAIL_BACKUP_MANAGED_TOPOLOGY", "compose")
	t.Setenv("URMAIL_BACKUP_MANAGED_PG_HOST", "")
	t.Setenv("URMAIL_BACKUP_S3_SECRET_KEY", "root-only-offsite-secret")

	peer, err := backupPeerDatabaseURL("postgres://urmail_migrate:private-fixture@pg:5432/urmail?sslmode=disable")
	if err != nil {
		t.Fatalf("managed compose DSN rejected: %v", err)
	}
	if !strings.HasPrefix(peer, managedBackupScheme+"://") || !strings.Contains(peer, "@pg:5432/urmail") {
		t.Fatalf("plan = %q", peer)
	}
	command, err := backupDatabaseCommand(context.Background(), peer)
	if err != nil {
		t.Fatal(err)
	}
	if command.Path != "/usr/bin/pg_dump" {
		t.Fatalf("managed backup must run pg_dump directly, got %s", command.Path)
	}
	joinedArgs := strings.Join(command.Args, " ")
	if strings.Contains(joinedArgs, "private-fixture") {
		t.Fatal("password leaked into argv")
	}
	env := strings.Join(command.Env, "\n")
	if !strings.Contains(env, "PGPASSWORD=private-fixture") || !strings.Contains(env, "PGOPTIONS=-c role="+managedPGRole) {
		t.Fatalf("managed command env = %q", command.Env)
	}
	if strings.Contains(env, "root-only-offsite-secret") {
		t.Fatal("managed pg_dump inherited offsite credentials")
	}

	for _, raw := range []string{
		"postgres://urmail:private-fixture@pg:5432/urmail?sslmode=disable",
		"postgres://urmail_platform:private-fixture@pg:5432/urmail?sslmode=disable",
		"postgres://urmail_migrate@pg:5432/urmail?sslmode=disable",
		"postgres://urmail_migrate:private-fixture@external.test:5432/urmail?sslmode=disable",
		"postgres://urmail_migrate:private-fixture@127.0.0.1:5432/urmail?sslmode=disable",
		"postgres://urmail_migrate:private-fixture@pg:6543/urmail?sslmode=disable",
		"postgres://urmail_migrate:private-fixture@pg:5432/urmail?options=-crole=postgres",
		"postgres://urmail_migrate:private-fixture@pg:5432/not-allowed!",
	} {
		if _, err := backupPeerDatabaseURL(raw); err == nil {
			t.Fatalf("managed topology accepted unsafe DSN %q", raw)
		}
	}
}

func TestBackupManagedTopologyRequiresExplicitDeclaration(t *testing.T) {
	t.Setenv("URMAIL_BACKUP_MANAGED_TOPOLOGY", "")
	t.Setenv("URMAIL_BACKUP_MANAGED_PG_HOST", "")
	if _, err := backupPeerDatabaseURL("postgres://urmail_migrate:private-fixture@pg:5432/urmail?sslmode=disable"); err == nil {
		t.Fatal("compose service host accepted without an explicit managed topology")
	}
	t.Setenv("URMAIL_BACKUP_MANAGED_TOPOLOGY", "kubernetes")
	if _, err := backupPeerDatabaseURL("postgres://urmail_migrate:private-fixture@pg:5432/urmail?sslmode=disable"); err == nil {
		t.Fatal("unknown topology accepted")
	}
}

func TestBackupManagedTopologyUsesTheConfiguredServiceName(t *testing.T) {
	t.Setenv("URMAIL_BACKUP_MANAGED_TOPOLOGY", "compose")
	t.Setenv("URMAIL_BACKUP_MANAGED_PG_HOST", "db")
	if _, err := backupPeerDatabaseURL("postgres://urmail_migrate:private-fixture@db:5432/urmail?sslmode=disable"); err != nil {
		t.Fatalf("configured service name rejected: %v", err)
	}
	if _, err := backupPeerDatabaseURL("postgres://urmail_migrate:private-fixture@pg:5432/urmail?sslmode=disable"); err == nil {
		t.Fatal("a service other than the configured one was accepted")
	}
}

func TestBackupManagedLoopbackHighPort(t *testing.T) {
	t.Setenv("URMAIL_BACKUP_MANAGED_TOPOLOGY", "loopback")
	t.Setenv("URMAIL_BACKUP_MANAGED_PG_HOST", "")
	t.Setenv("URMAIL_BACKUP_MANAGED_PG_PORT", "55432")
	t.Setenv("URMAIL_BACKUP_S3_SECRET_KEY", "root-only-offsite-secret")

	for _, dsn := range []string{
		"postgres://urmail_migrate:private-fixture@127.0.0.1:55432/urmail?sslmode=disable",
		"postgres://urmail_migrate:private-fixture@localhost:55432/urmail?sslmode=disable",
		"postgres://urmail_migrate:private-fixture@[::1]:55432/urmail?sslmode=disable",
	} {
		peer, err := backupPeerDatabaseURL(dsn)
		if err != nil {
			t.Fatalf("loopback DSN %q rejected: %v", dsn, err)
		}
		command, err := backupDatabaseCommand(context.Background(), peer)
		if err != nil {
			t.Fatalf("loopback command for %q: %v", dsn, err)
		}
		if command.Path != "/usr/bin/pg_dump" {
			t.Fatalf("loopback backup must run pg_dump directly, got %s", command.Path)
		}
		env := strings.Join(command.Env, "\n")
		if !strings.Contains(env, "PGOPTIONS=-c role="+managedPGRole) || !strings.Contains(env, "PGPASSWORD=private-fixture") {
			t.Fatalf("loopback env = %q", command.Env)
		}
		if strings.Contains(strings.Join(command.Args, " "), "private-fixture") {
			t.Fatal("password leaked into argv")
		}
	}

	for _, dsn := range []string{
		"postgres://urmail_migrate:private-fixture@127.0.0.1:5432/urmail?sslmode=disable",
		"postgres://urmail_migrate:private-fixture@0.0.0.0:55432/urmail?sslmode=disable",
		"postgres://urmail_migrate:private-fixture@10.1.2.3:55432/urmail?sslmode=disable",
		"postgres://urmail_migrate:private-fixture@external.test:55432/urmail?sslmode=disable",
		"postgres://urmail:private-fixture@127.0.0.1:55432/urmail?sslmode=disable",
		"postgres://urmail_migrate@127.0.0.1:55432/urmail?sslmode=disable",
		"postgres://urmail_migrate:private-fixture@127.0.0.1:55432/urmail?options=-crole=postgres",
	} {
		if _, err := backupPeerDatabaseURL(dsn); err == nil {
			t.Fatalf("loopback topology accepted unsafe DSN %q", dsn)
		}
	}

	t.Setenv("URMAIL_BACKUP_MANAGED_PG_HOST", "pg")
	if _, err := backupPeerDatabaseURL("postgres://urmail_migrate:private-fixture@127.0.0.1:55432/urmail?sslmode=disable"); err == nil {
		t.Fatal("loopback topology accepted the compose host override")
	}
}

func TestBackupManagedPortSwitchIsTopologyScoped(t *testing.T) {
	t.Setenv("URMAIL_BACKUP_MANAGED_TOPOLOGY", "compose")
	t.Setenv("URMAIL_BACKUP_MANAGED_PG_HOST", "")
	t.Setenv("URMAIL_BACKUP_MANAGED_PG_PORT", "55432")
	if _, err := backupPeerDatabaseURL("postgres://urmail_migrate:private-fixture@pg:55432/urmail?sslmode=disable"); err == nil {
		t.Fatal("compose topology accepted the loopback port switch")
	}
	t.Setenv("URMAIL_BACKUP_MANAGED_TOPOLOGY", "")
	t.Setenv("URMAIL_BACKUP_MANAGED_PG_PORT", "")
	if _, err := backupPeerDatabaseURL("postgres://urmail_migrate:private-fixture@127.0.0.1:55432/urmail?sslmode=disable"); err == nil {
		t.Fatal("undeclared topology accepted a non-5432 port")
	}
	if _, err := backupPeerDatabaseURL("postgres://urmail_migrate:private-fixture@127.0.0.1:5432/urmail?sslmode=disable"); err != nil {
		t.Fatalf("undeclared 5432 must keep working: %v", err)
	}
	t.Setenv("URMAIL_BACKUP_MANAGED_TOPOLOGY", "loopback")
	t.Setenv("URMAIL_BACKUP_MANAGED_PG_PORT", "80")
	if _, err := backupPeerDatabaseURL("postgres://urmail_migrate:private-fixture@127.0.0.1:80/urmail?sslmode=disable"); err == nil {
		t.Fatal("loopback topology accepted a privileged port")
	}
}
