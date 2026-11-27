package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/datamigrate"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

func TestConfigPrepareRejectsBeforeAnyPrivilegedWork(t *testing.T) {
	// The privilege check is intentionally not overridden by URMAIL_ALLOW_TEST_USER.
	t.Setenv("URMAIL_ALLOW_TEST_USER", "1")
	err := runConfigPrepare(context.Background(), []string{"--force"})
	if err == nil || (!strings.Contains(err.Error(), "requires root") && !strings.Contains(err.Error(), "usage:")) {
		t.Fatalf("unexpected prepare argument result: %v", err)
	}
}

func TestFirstBootPrepareAllowsNonRootButRejectsUnknownFlags(t *testing.T) {
	t.Setenv("URMAIL_ALLOW_TEST_USER", "1")
	// The container identity owns its config volume, so first boot may run
	// unprivileged; any later failure must not be the root gate.
	if err := runConfigPrepare(context.Background(), []string{"--first-boot"}); err != nil && strings.Contains(err.Error(), "requires root") {
		t.Fatalf("first-boot refused the container identity: %v", err)
	}
	for _, args := range [][]string{{"--first-boot", "--skip-preflight"}, {"--skip-backup"}} {
		err := runConfigPrepare(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("args %v were accepted: %v", args, err)
		}
	}
	// The fenced upgrade path stays root-only.
	if err := runConfigPrepare(context.Background(), []string{}); err == nil || !strings.Contains(err.Error(), "requires root") {
		t.Fatalf("fenced prepare no longer requires root: %v", err)
	}
}

func TestFirstBootPrepareRefusesActiveUpgradeFence(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("first-boot fence refusal is checked after the root gate")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "upgrade.hold"), []byte("held\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("URMAIL_MAINTENANCE_ROOT", root)
	err := runConfigPrepare(context.Background(), []string{"--first-boot"})
	if err == nil || !strings.Contains(err.Error(), "upgrade fence is active") {
		t.Fatalf("active fence result: %v", err)
	}
}

func TestConfigPrepareSQLiteArrayDataBothPaths(t *testing.T) {
	ctx := context.Background()
	sqlitePath := filepath.Join(t.TempDir(), "urmail.sqlite")
	raw, err := sqlitedb.Open(ctx, sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.Migrate(ctx, raw, migrations_sqlite.FS, "."); err != nil {
		t.Fatal(err)
	}
	conv := func(pgLiteral string) string {
		v, err := datamigrate.ConvertJSONArray(pgLiteral)
		if err != nil {
			t.Fatalf("ConvertJSONArray(%q): %v", pgLiteral, err)
		}
		return v.(string)
	}
	for _, stmt := range []string{
		`INSERT INTO brands(slug,name) VALUES('default','Default')`,
		`INSERT INTO tenants(brand_id,name,slug,status) VALUES(1,'Tenant','tenant','active')`,
		`INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,mode) VALUES(1,'example.test','active','tok',1,'delegated')`,
		`UPDATE domains SET provisioning_state='ready' WHERE id=1`,
		`INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES(1,1,'u','u@example.test','h','active')`,
	} {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.ExecContext(ctx,
		`INSERT INTO domain_transport_security(tenant_id,domain_id,mtasts_policy_id,mtasts_mode,tls_rpt_rua,enabled,policy_mx,max_age,published_at,cache_safe_after) VALUES(1,1,'p1','testing','mailto:r@example.test',1,?,86400,1,1)`,
		conv(`{mx1.example.test,box.example.test}`),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx,
		`INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,sender_logins) VALUES(1,1,'a','u@example.test',?)`,
		conv(`{u@example.test}`),
	); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	cfg := &config.Config{
		DBEngine:           "sqlite",
		SQLitePath:         sqlitePath,
		MyHostname:         "box.example.test",
		PrimaryDomain:      "example.test",
		StorageRoot:        "/var/mail",
		DatabaseURLDovecot: "host=127.0.0.1 dbname=urmail",
	}
	pool, closePool, err := openPlatformStorePool(ctx, cfg)
	if err != nil {
		t.Fatalf("openPlatformStorePool sqlite: %v", err)
	}
	defer closePool()

	// Allow: assembleApplyParams must scan policy_mx and sender_logins without error.
	paramsRaw, err := assembleApplyParams(ctx, cfg, pool)
	if err != nil {
		t.Fatalf("assembleApplyParams sqlite: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(paramsRaw, &decoded); err != nil {
		t.Fatalf("unmarshal apply params: %v", err)
	}

	// Refuse: non-first-boot PrepareOfflineWithPool refuses non-root when euid != 0.
	if os.Geteuid() != 0 {
		if _, err := agentd.PrepareOfflineWithPool(ctx, cfg, pool, paramsRaw); err == nil || !strings.Contains(err.Error(), "requires root") {
			t.Fatalf("expected root refusal, got %v", err)
		}
	}
	// Refuse: nil pool is rejected fail-closed on both paths.
	if _, err := agentd.PrepareOfflineFirstBoot(ctx, cfg, nil, paramsRaw); err == nil {
		t.Fatal("expected nil pool refusal on first-boot")
	}
}
