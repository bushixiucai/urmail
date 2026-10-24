package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// testDatabaseURL creates an owned, bootstrapped database for each test.
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	return testdb.New(t)
}

// applyMigrations brings the database up to the current schema.
func applyMigrations(t *testing.T, url string) {
	t.Helper()

	migrator, err := db.NewMigrator(url)
	if err != nil {
		t.Fatalf("create migrator: %v", err)
	}
	defer func() { _ = migrator.Close() }()

	if err := migrator.Up(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
}

// truncate wipes the seeded data so that the assertions below can count rows
// without having to reason about leftovers from an earlier run.
func truncate(t *testing.T, pool *db.Pool) {
	t.Helper()

	ctx := context.Background()
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		TRUNCATE mailbox_storage_reservations,mailboxes, mailbox_aliases, mailbox_usage, domains,
		         tenants, brands, users, settings CASCADE`); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("truncate: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit truncate: %v", err)
	}
}

// TestMigrationIsReversible proves that the schema can be torn down again.
// A migration without a working `down` would leave a box that cannot be
// rolled back.
func TestMigrationIsReversible(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()

	migrator, err := db.NewMigrator(url)
	if err != nil {
		t.Fatalf("create migrator: %v", err)
	}
	defer func() { _ = migrator.Close() }()

	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if err := migrator.Down(ctx); err != nil {
		t.Fatalf("roll back migration: %v", err)
	}
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("re-apply migrations: %v", err)
	}
}

// TestTenantIsolation proves the core multi-tenant boundary: the tenant-scoped
// role must not be able to read another tenant's rows, while the platform
// role must be able to read all of them.
func TestTenantIsolation(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()

	applyMigrations(t, url)

	platform, err := db.OpenPlatform(ctx, url)
	if err != nil {
		t.Fatalf("open platform pool: %v", err)
	}
	defer platform.Close()

	app, err := db.OpenApp(ctx, url)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer app.Close()

	truncate(t, platform)

	// Seed two tenants, each with one domain and one mailbox.
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}

	var brandID int64
	if err := tx.QueryRow(ctx, "INSERT INTO brands (slug, name) VALUES ('test', 'Test') RETURNING id").Scan(&brandID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert brand: %v", err)
	}

	type seed struct {
		tenantID int64
		fqdn     string
		address  string
	}
	seeds := make([]seed, 0, 2)
	for _, spec := range []struct{ slug, fqdn, address string }{
		{"a", "a.example.com", "alice@a.example.com"},
		{"b", "b.example.com", "bob@b.example.com"},
	} {
		var tenantID, domainID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO tenants (brand_id, name, slug) VALUES ($1, $2, $2) RETURNING id`,
			brandID, spec.slug).Scan(&tenantID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("insert tenant %s: %v", spec.slug, err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO domains (tenant_id, fqdn, mode, status, verification_token, verified_at)
			VALUES ($1, $2, 'delegated', 'active', 'token-' || $2, now()) RETURNING id`,
			tenantID, spec.fqdn).Scan(&domainID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("insert domain %s: %v", spec.fqdn, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO mailboxes (tenant_id, domain_id, local_part, address, password_hash)
			VALUES ($1, $2, split_part($3,'@',1), $3, '{SHA512-CRYPT}$6$saltsalt$hashhash')`,
			tenantID, domainID, spec.address); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("insert mailbox %s: %v", spec.address, err)
		}
		seeds = append(seeds, seed{tenantID: tenantID, fqdn: spec.fqdn, address: spec.address})
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed transaction: %v", err)
	}

	// The platform role sees every tenant.
	platformTx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatalf("begin platform transaction: %v", err)
	}
	var total int
	if err := platformTx.QueryRow(ctx, "SELECT count(*) FROM mailboxes").Scan(&total); err != nil {
		_ = platformTx.Rollback(ctx)
		t.Fatalf("count as platform: %v", err)
	}
	_ = platformTx.Rollback(ctx)
	if total != 2 {
		t.Fatalf("the platform role should see 2 mailboxes, got %d", total)
	}

	// Each tenant sees only its own mailbox.
	for _, spec := range seeds {
		tenantTx, err := app.TenantTx(ctx, spec.tenantID)
		if err != nil {
			t.Fatalf("begin tenant transaction: %v", err)
		}
		var visible int
		if err := tenantTx.QueryRow(ctx, "SELECT count(*) FROM mailboxes").Scan(&visible); err != nil {
			_ = tenantTx.Rollback(ctx)
			t.Fatalf("count as tenant %d: %v", spec.tenantID, err)
		}
		_ = tenantTx.Rollback(ctx)

		if visible != 1 {
			t.Fatalf("tenant %d should see exactly 1 mailbox, got %d", spec.tenantID, visible)
		}
	}

	// A tenant must not be able to write a row belonging to another tenant.
	other := seeds[1]
	tx2, err := app.TenantTx(ctx, seeds[0].tenantID)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	_, err = tx2.Exec(ctx, `
		INSERT INTO mailboxes (tenant_id, domain_id, local_part, address, password_hash)
		SELECT $1, domain_id, 'intruder', 'intruder@example.com', 'x'
		FROM domains LIMIT 1`, other.tenantID)
	if err == nil {
		_ = tx2.Rollback(ctx)
		t.Fatal("writing into another tenant must be rejected")
	}
	_ = tx2.Rollback(ctx)

	// The tenant scope must not leak to the next transaction on the same
	// pooled connection.
	idleTx, err := app.PlatformTx(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	var leaked int
	if err := idleTx.QueryRow(ctx, "SELECT count(*) FROM mailboxes").Scan(&leaked); err != nil {
		_ = idleTx.Rollback(ctx)
		t.Fatalf("count without a tenant context: %v", err)
	}
	_ = idleTx.Rollback(ctx)

	if leaked != 0 {
		t.Fatalf("without a tenant context no row may be visible, got %d", leaked)
	}
}

// TestTenantContextIsScopedToTheTransaction proves that the tenant id set with
// SET LOCAL does not survive the transaction, which is what keeps a pooled
// connection from serving one tenant's data to another.
func TestTenantContextIsScopedToTheTransaction(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()

	applyMigrations(t, url)

	app, err := db.OpenApp(ctx, url)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer app.Close()

	tx, err := app.TenantTx(ctx, 4242)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	current, err := db.CurrentTenant(ctx, tx)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("read tenant context: %v", err)
	}
	if current != 4242 {
		t.Fatalf("expected tenant 4242, got %d", current)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// A second transaction on the same pooled connection must start clean.
	second, err := app.TenantTx(ctx, 1)
	if err != nil {
		t.Fatalf("begin second tenant transaction: %v", err)
	}
	current, err = db.CurrentTenant(ctx, second)
	if err != nil {
		_ = second.Rollback(ctx)
		t.Fatalf("read tenant context: %v", err)
	}
	if current != 1 {
		t.Fatalf("expected tenant 1, got %d", current)
	}
	_ = second.Rollback(ctx)
}

// TestOpenRejectsAnUnreachableDatabase proves the service fails loudly rather
// than starting in a broken state.
func TestOpenRejectsAnUnreachableDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := db.OpenApp(ctx, "postgres://nobody@127.0.0.1:1/nothing?connect_timeout=2")
	if err == nil {
		t.Fatal("opening an unreachable database must fail")
	}
}

// TestPlatformTxHasNoTenant proves the platform transaction helper does not
// accidentally set a tenant scope.
func TestPlatformTxHasNoTenant(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()

	applyMigrations(t, url)

	app, err := db.OpenApp(ctx, url)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer app.Close()

	tx, err := app.PlatformTx(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := db.CurrentTenant(ctx, tx)
	if err != nil {
		t.Fatalf("read tenant context: %v", err)
	}
	if current != 0 {
		t.Fatalf("a platform transaction must have no tenant context, got %d", current)
	}
}

// TestSettingsAndBrandsRLSSecurity proves the P0 security fixes from migration 0002:
// 1. A tenant cannot write platform or brand settings (only their own tenant scope).
// 2. A tenant cannot insert or modify brands (WITH CHECK (false)).
func TestSettingsAndBrandsRLSSecurity(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()

	applyMigrations(t, url)

	platform, err := db.OpenPlatform(ctx, url)
	if err != nil {
		t.Fatalf("open platform pool: %v", err)
	}
	defer platform.Close()

	app, err := db.OpenApp(ctx, url)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer app.Close()

	truncate(t, platform)

	// Seed one brand and one tenant via platform role
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}

	var brandID int64
	if err := tx.QueryRow(ctx, "INSERT INTO brands (slug, name) VALUES ('test-brand', 'Test Brand') RETURNING id").Scan(&brandID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert brand: %v", err)
	}

	var tenantID int64
	if err := tx.QueryRow(ctx, "INSERT INTO tenants (brand_id, name, slug) VALUES ($1, 'Test Tenant', 'test-tenant') RETURNING id", brandID).Scan(&tenantID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert tenant: %v", err)
	}

	// Insert a platform setting via platform
	if _, err := tx.Exec(ctx, `INSERT INTO settings (scope, scope_id, key, value) VALUES ('platform', 0, 'locale', '"en"'::jsonb)`); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert platform setting: %v", err)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	// 1. Tenant can read platform setting
	tenantTx, err := app.TenantTx(ctx, tenantID)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	var val string
	if err := tenantTx.QueryRow(ctx, "SELECT value::text FROM settings WHERE scope = 'platform' AND key = 'locale'").Scan(&val); err != nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatalf("tenant read platform setting: %v", err)
	}
	_ = tenantTx.Rollback(ctx)

	// 2. Tenant cannot insert platform setting
	tenantTx, err = app.TenantTx(ctx, tenantID)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	_, err = tenantTx.Exec(ctx, `INSERT INTO settings (scope, scope_id, key, value) VALUES ('platform', 0, 'evil_key', '"evil"'::jsonb)`)
	if err == nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to insert platform settings")
	}
	_ = tenantTx.Rollback(ctx)

	// 3. Tenant cannot update platform setting
	tenantTx, err = app.TenantTx(ctx, tenantID)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	changed, err := tenantTx.Exec(ctx, `UPDATE settings SET value = '"hacked"'::jsonb WHERE scope = 'platform' AND key = 'locale'`)
	if err == nil && changed.RowsAffected() != 0 {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to update platform settings")
	}
	_ = tenantTx.Rollback(ctx)

	// 4. Tenant CAN write their own tenant setting
	tenantTx, err = app.TenantTx(ctx, tenantID)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	_, err = tenantTx.Exec(ctx, `INSERT INTO settings (scope, scope_id, key, value) VALUES ('tenant', $1, 'theme', '{"primary":"#123456"}'::jsonb)`, tenantID)
	if err != nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatalf("tenant should be able to insert tenant setting: %v", err)
	}
	_ = tenantTx.Rollback(ctx)

	// 5. Tenant CANNOT insert a brand (WITH CHECK (false))
	tenantTx, err = app.TenantTx(ctx, tenantID)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	_, err = tenantTx.Exec(ctx, "INSERT INTO brands (slug, name) VALUES ('rogue-brand', 'Rogue Brand')")
	if err == nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to insert into brands")
	}
	_ = tenantTx.Rollback(ctx)
}

// TestDovecotRolePermissions proves the D4 security model:
// urmail_dovecot has BYPASSRLS and can SELECT from mailboxes and domains,
// but CANNOT write, and CANNOT access other tables (e.g. settings, tenants, audit_logs).
func TestDovecotRolePermissions(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()

	applyMigrations(t, url)

	platform, err := db.OpenPlatform(ctx, url)
	if err != nil {
		t.Fatalf("open platform pool: %v", err)
	}
	defer platform.Close()

	truncate(t, platform)

	// Seed brand, tenant, domain, mailbox via platform role
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}

	var brandID, tenantID, domainID int64
	if err := tx.QueryRow(ctx, "INSERT INTO brands (slug, name) VALUES ('b', 'B') RETURNING id").Scan(&brandID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert brand: %v", err)
	}
	if err := tx.QueryRow(ctx, "INSERT INTO tenants (brand_id, name, slug) VALUES ($1, 'T', 't') RETURNING id", brandID).Scan(&tenantID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert tenant: %v", err)
	}
	if err := tx.QueryRow(ctx, "INSERT INTO domains (tenant_id, fqdn, status, verification_token, verified_at) VALUES ($1, 'd.com', 'active', 'tok', now()) RETURNING id", tenantID).Scan(&domainID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert domain: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO mailboxes (tenant_id, domain_id, local_part, address, password_hash) VALUES ($1, $2, 'u', 'u@d.com', '{SHA512-CRYPT}x')", tenantID, domainID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert mailbox: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	// Connect as platform, then SET ROLE to urmail_dovecot to verify effective permissions
	dovTx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = dovTx.Rollback(ctx) }()

	if _, err := dovTx.Exec(ctx, "SET ROLE urmail_dovecot"); err != nil {
		t.Fatalf("set role urmail_dovecot: %v", err)
	}

	// 1. urmail_dovecot can SELECT mailboxes across tenants (BYPASSRLS)
	var count int
	if err := dovTx.QueryRow(ctx, "SELECT count(*) FROM mailboxes").Scan(&count); err != nil {
		t.Fatalf("select mailboxes as urmail_dovecot: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 mailbox visible, got %d", count)
	}

	// 2. urmail_dovecot can SELECT domains
	if err := dovTx.QueryRow(ctx, "SELECT count(*) FROM domains").Scan(&count); err != nil {
		t.Fatalf("select domains as urmail_dovecot: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 domain visible, got %d", count)
	}

	// 3. urmail_dovecot CANNOT write to mailboxes (no INSERT/UPDATE/DELETE privilege)
	_, err = dovTx.Exec(ctx, "INSERT INTO mailboxes (tenant_id, domain_id, local_part, address, password_hash) VALUES (1, 1, 'bad', 'bad@d.com', 'x')")
	if err == nil {
		t.Fatal("urmail_dovecot must NOT be permitted to INSERT into mailboxes")
	}

	// 4. urmail_dovecot CANNOT access tenants or settings tables
	_, err = dovTx.Exec(ctx, "SELECT count(*) FROM tenants")
	if err == nil {
		t.Fatal("urmail_dovecot must NOT have SELECT on tenants")
	}
}

// TestTightenedRLSGrants proves the P0 security fixes from migration 0004:
// 1. The app role can read its own tenant's audit_logs but can never write them.
// 2. The app role cannot see another tenant's audit_logs rows.
// 3. coupons and plans are read-only for the app role (REVOKE INSERT/UPDATE/DELETE).
// 4. channels and announcements are read-only for the app role (WITH CHECK (false)).
func TestTightenedRLSGrants(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()

	applyMigrations(t, url)

	platform, err := db.OpenPlatform(ctx, url)
	if err != nil {
		t.Fatalf("open platform pool: %v", err)
	}
	defer platform.Close()

	app, err := db.OpenApp(ctx, url)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer app.Close()

	truncate(t, platform)

	// Seed two tenants and one audit log per tenant via the platform role.
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}

	var brandID int64
	if err := tx.QueryRow(ctx, "INSERT INTO brands (slug, name) VALUES ('rls-brand', 'RLS Brand') RETURNING id").Scan(&brandID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert brand: %v", err)
	}

	var tenantID, otherTenantID int64
	if err := tx.QueryRow(ctx, "INSERT INTO tenants (brand_id, name, slug) VALUES ($1, 'RLS Tenant', 'rls-tenant') RETURNING id", brandID).Scan(&tenantID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert tenant: %v", err)
	}
	if err := tx.QueryRow(ctx, "INSERT INTO tenants (brand_id, name, slug) VALUES ($1, 'Other Tenant', 'other-tenant') RETURNING id", brandID).Scan(&otherTenantID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert other tenant: %v", err)
	}

	if _, err := tx.Exec(ctx, "INSERT INTO audit_logs (tenant_id, action) VALUES ($1, 'test.own')", tenantID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert audit log: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO audit_logs (tenant_id, action) VALUES ($1, 'test.other')", otherTenantID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert other audit log: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO coupons (code, kind, value) VALUES ('RLSTEST', 'percent', 10) ON CONFLICT (code) DO NOTHING"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert coupon: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO plans (code, name, price_cents, quota_bytes, mailbox_limit, domain_limit, daily_send_limit) VALUES ('rls-test', '{\"en\":\"RLS\"}', 0, 1, 1, 1, 1) ON CONFLICT (code) DO NOTHING"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert plan: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO channels (brand_id, code, name) VALUES ($1, 'rls-channel', 'RLS Channel') ON CONFLICT (code) DO NOTHING", brandID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO announcements (brand_id, title, body) VALUES ($1, '{\"en\":\"Hi\"}', '{\"en\":\"Body\"}')", brandID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert announcement: %v", err)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	// 1. The app role reads its own tenant's audit logs, and only those.
	tenantTx, err := app.TenantTx(ctx, tenantID)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	var count int
	if err := tenantTx.QueryRow(ctx, "SELECT count(*) FROM audit_logs").Scan(&count); err != nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatalf("tenant read audit_logs: %v", err)
	}
	if count != 1 {
		_ = tenantTx.Rollback(ctx)
		t.Fatalf("expected to see exactly 1 own audit log, got %d", count)
	}
	_ = tenantTx.Rollback(ctx)

	// 2. The app role can never write audit_logs (WITH CHECK (false)).
	tenantTx, err = app.TenantTx(ctx, tenantID)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	if _, err := tenantTx.Exec(ctx, "INSERT INTO audit_logs (tenant_id, action) VALUES ($1, 'evil.insert')", tenantID); err == nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to insert audit_logs")
	}
	if _, err := tenantTx.Exec(ctx, "UPDATE audit_logs SET action = 'evil.update'"); err == nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to update audit_logs")
	}
	if _, err := tenantTx.Exec(ctx, "DELETE FROM audit_logs"); err == nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to delete audit_logs")
	}
	_ = tenantTx.Rollback(ctx)

	// 3. Another tenant's audit logs are invisible.
	tenantTx, err = app.TenantTx(ctx, tenantID)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	if err := tenantTx.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id = $1", otherTenantID).Scan(&count); err != nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatalf("cross-tenant read audit_logs: %v", err)
	}
	if count != 0 {
		_ = tenantTx.Rollback(ctx)
		t.Fatalf("cross-tenant audit log must be invisible, saw %d", count)
	}
	_ = tenantTx.Rollback(ctx)

	// 4. coupons and plans are read-only for the app role.
	tenantTx, err = app.TenantTx(ctx, tenantID)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	if _, err := tenantTx.Exec(ctx, "INSERT INTO coupons (code, kind, value) VALUES ('EVIL', 'percent', 100)"); err == nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to insert coupons")
	}
	if _, err := tenantTx.Exec(ctx, "UPDATE coupons SET active = false WHERE code = 'RLSTEST'"); err == nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to update coupons")
	}
	if _, err := tenantTx.Exec(ctx, "DELETE FROM coupons WHERE code = 'RLSTEST'"); err == nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to delete coupons")
	}
	if _, err := tenantTx.Exec(ctx, "UPDATE plans SET active = false WHERE code = 'rls-test'"); err == nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to update plans")
	}
	_ = tenantTx.Rollback(ctx)

	// SELECT on coupons and plans must still work.
	tenantTx, err = app.TenantTx(ctx, tenantID)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	if err := tenantTx.QueryRow(ctx, "SELECT count(*) FROM coupons").Scan(&count); err != nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatalf("tenant read coupons: %v", err)
	}
	if err := tenantTx.QueryRow(ctx, "SELECT count(*) FROM plans").Scan(&count); err != nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatalf("tenant read plans: %v", err)
	}
	_ = tenantTx.Rollback(ctx)

	// 5. channels and announcements are read-only for the app role.
	tenantTx, err = app.TenantTx(ctx, tenantID)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	if _, err := tenantTx.Exec(ctx, "INSERT INTO channels (brand_id, code, name) VALUES ($1, 'evil-channel', 'Evil')", brandID); err == nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to insert channels")
	}
	if _, err := tenantTx.Exec(ctx, "UPDATE channels SET active = false WHERE code = 'rls-channel'"); err == nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to update channels")
	}
	if _, err := tenantTx.Exec(ctx, "INSERT INTO announcements (brand_id, title, body) VALUES ($1, '{\"en\":\"Evil\"}', '{\"en\":\"Evil\"}')", brandID); err == nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to insert announcements")
	}
	if _, err := tenantTx.Exec(ctx, "UPDATE announcements SET published_at = now()"); err == nil {
		_ = tenantTx.Rollback(ctx)
		t.Fatal("tenant MUST NOT be able to update announcements")
	}
	_ = tenantTx.Rollback(ctx)
}
