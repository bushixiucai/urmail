package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestSetupCreatesPendingTenantAndRequiresExplicitFiniteGift(t *testing.T) {
	p, a := testdb.OpenMigrated(t)
	ctx := context.Background()
	t.Setenv("URMAIL_DATABASE_URL_PLATFORM", p.Config().ConnString())
	t.Setenv("URMAIL_DATABASE_URL", p.Config().ConnString())
	t.Setenv("URMAIL_POLICY_ENABLED", "true")
	t.Setenv("URMAIL_MYHOSTNAME", "mail.example.test")
	args := []string{"--email", "bootstrap@example.test", "--password", "fixture-initial-password-12345"}
	if err := runSetup(ctx, args); err != nil {
		t.Fatal(err)
	}
	var tenant int64
	var status string
	var count int
	if err := p.QueryRow(ctx, "SELECT id,status FROM tenants WHERE slug='default'").Scan(&tenant, &status); err != nil || status != "pending" {
		t.Fatalf("fresh tenant status=%s err=%v", status, err)
	}
	var allowed bool
	if err := p.QueryRow(ctx, "SELECT urmail_mail_allowed($1)", tenant).Scan(&allowed); err != nil || allowed {
		t.Fatalf("pending tenant granted mail: %v %v", allowed, err)
	}
	var owner int64
	if err := p.QueryRow(ctx, "SELECT owner_user_id FROM tenants WHERE id=$1", tenant).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	tx, err := a.UserTx(ctx, tenant, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO domains(tenant_id,fqdn,mode,status,verification_token) VALUES($1,'bootstrap-proof.test','delegated','pending_dns',$2)", tenant, strings.Repeat("a", 64)); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("pending owner could not enroll a domain without a gift: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "SELECT urmail_mail_allowed($1)", tenant).Scan(&allowed); err != nil || allowed {
		t.Fatalf("pending enrollment granted mail: %v %v", allowed, err)
	}
	until := time.Now().UTC().Add(14 * 24 * time.Hour).Truncate(time.Second)
	gifted := append(append([]string{}, args...), "--free-until", until.Format(time.RFC3339), "--free-reason", "Explicit finite operator gift in acceptance fixture")
	if err := runSetup(ctx, gifted); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "SELECT status FROM tenants WHERE id=$1", tenant).Scan(&status); err != nil || status != "active" {
		t.Fatalf("explicit grant status=%s err=%v", status, err)
	}
	if err := runSetup(ctx, args); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "SELECT status FROM tenants WHERE id=$1", tenant).Scan(&status); err != nil || status != "active" {
		t.Fatalf("idempotent setup reverted tenant %s %v", status, err)
	}
	if err := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='SETUP platform.bootstrap'", tenant).Scan(&count); err != nil || count != 3 {
		t.Fatalf("bootstrap audits=%d err=%v", count, err)
	}
}

func TestSetupPreflightReadsInstalledEnvironmentFile(t *testing.T) {
	p, _ := testdb.OpenMigrated(t)
	uri := strings.Replace(p.Config().ConnString(), "postgres://", "postgresql://", 1)
	path := filepath.Join(t.TempDir(), "urmaild.env")
	content := "URMAIL_DATABASE_URL=" + uri + "\nURMAIL_DATABASE_URL_PLATFORM=" + uri + "\nURMAIL_MYHOSTNAME=box.envfile.test\nURMAIL_POLICY_ENABLED=true\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("URMAIL_ENV_FILE", path)
	for _, key := range []string{"URMAIL_DATABASE_URL", "URMAIL_DATABASE_URL_PLATFORM", "URMAIL_MYHOSTNAME"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	if err := runSetup(t.Context(), []string{"--email", "envfile@example.test", "--password", "isolated-envfile-password-1234"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := p.QueryRow(t.Context(), "SELECT count(*) FROM users WHERE email='envfile@example.test'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("installed env file did not bootstrap user: count=%d err=%v", count, err)
	}
}
