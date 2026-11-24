package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

func newSetupSQLite(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "setup.db")
	db, err := sqlitedb.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlitedb.Migrate(context.Background(), db, migrations_sqlite.FS, "."); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	t.Setenv("URMAIL_DB_ENGINE", "sqlite")
	t.Setenv("URMAIL_SQLITE_PATH", path)
	t.Setenv("URMAIL_LISTEN_ADDR", "127.0.0.1:18098")
	t.Setenv("URMAIL_POLICY_ENABLED", "true")
	t.Setenv("URMAIL_MYHOSTNAME", "drill.test")
	return path, db
}

// TestSetupCreatesFirstAdminOnSQLite is the deployment path DevOps needs:
// an empty verified SQLite database, then `urmailctl setup` creates the
// brand, tenant, owner and platform administrator without a PG engine.
func TestSetupCreatesFirstAdminOnSQLite(t *testing.T) {
	_, raw := newSetupSQLite(t)
	if err := runSetup(context.Background(), []string{"--email", "admin@drill.test", "--password", "drill-password-123", "--brand", "acme", "--tenant", "acme", "--tenant-name", "ACME"}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	var tenantID, userID, ownerID int64
	var status, role, email string
	if err := raw.QueryRow(`SELECT id,status,owner_user_id FROM tenants WHERE slug='acme'`).Scan(&tenantID, &status, &ownerID); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT id,email FROM users WHERE email='admin@drill.test'`).Scan(&userID, &email); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT role FROM user_tenant_roles WHERE tenant_id=? AND user_id=?`, tenantID, userID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	var admins, audits int
	_ = raw.QueryRow(`SELECT count(*) FROM platform_admins WHERE user_id=?`, userID).Scan(&admins)
	_ = raw.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='SETUP platform.bootstrap' AND tenant_id=?`, tenantID).Scan(&audits)
	if status != "pending" || ownerID != userID || role != "owner" || admins != 1 || audits != 1 {
		t.Fatalf("setup rows: tenant=%d status=%q owner=%d user=%d role=%q admins=%d audits=%d", tenantID, status, ownerID, userID, role, admins, audits)
	}

	// The first domain goes through the product CLI path and its DKIM
	// generator; no password is needed for an existing administrator.
	if err := runSetup(context.Background(), []string{"--domain", "first.example.test", "--email", "admin@drill.test"}); err != nil {
		t.Fatalf("setup --domain: %v", err)
	}
	var stored string
	if err := raw.QueryRow(`SELECT dkim_private_key FROM domains WHERE fqdn='first.example.test'`).Scan(&stored); err != nil || len(stored) < 100 || !strings.Contains(stored, "RSA PRIVATE KEY") {
		t.Fatalf("domain dkim_private_key len=%d err=%v", len(stored), err)
	}
}

// TestSetupNonPlatformScopeIsRefusedBeforeWrites proves the deployment
// negative control: URMAIL_SETUP_SCOPE=app fails before a write and leaves
// the database unchanged.
func TestSetupNonPlatformScopeIsRefusedBeforeWrites(t *testing.T) {
	_, raw := newSetupSQLite(t)
	before := setupDBFileSHA256(t, raw)
	t.Setenv("URMAIL_SETUP_SCOPE", "app")
	err := runSetup(context.Background(), []string{"--email", "neg@drill.test", "--password", "drill-password-123", "--brand", "default", "--tenant", "default"})
	if err == nil || !strings.Contains(err.Error(), "platform scope required") {
		t.Fatalf("non-platform setup must be refused, got %v", err)
	}
	if after := setupDBFileSHA256(t, raw); after != before {
		t.Fatalf("refused setup changed the database: %s -> %s", before, after)
	}
}

// TestSetupSQLiteKeepsPlatformScopeEnforced: a tenant-scoped context must
// still be rejected for platform setup statements.
func TestSetupSQLiteKeepsPlatformScopeEnforced(t *testing.T) {
	_, raw := newSetupSQLite(t)
	pool, err := db.NewSQLitePool(raw, isolation.ScopeTenant)
	if err != nil {
		t.Fatal(err)
	}
	ctx := isolation.WithScope(context.Background(), isolation.ScopeContext{Scope: isolation.ScopeTenant, TenantID: 1})
	q := db.Queries(pool)
	if _, err = q.PlatformSetupTenant(ctx, store.PlatformSetupTenantParams{BrandID: 1, Name: "ACME", Slug: "acme"}); err == nil || !strings.Contains(err.Error(), "platform scope required") {
		t.Fatalf("platform method accepted a tenant context: %v", err)
	}
}

func setupDBFileSHA256(t *testing.T, raw *sql.DB) string {
	t.Helper()
	path := ""
	if err := raw.QueryRow("PRAGMA database_list").Scan(new(int), new(string), &path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
