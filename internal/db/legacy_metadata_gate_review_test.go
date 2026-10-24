package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

type legacyGateFixture struct {
	dsn                   string
	p                     *db.Pool
	tenant, actor, domain int64
}

func newLegacyGateFixture(t *testing.T, grant bool) legacyGateFixture {
	t.Helper()
	f := legacyGateFixture{dsn: testDatabaseURL(t)}
	applyMigrations(t, f.dsn)
	var err error
	f.p, err = db.OpenPlatform(t.Context(), f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.p.Close)
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var n int64
		if e := f.p.QueryRow(t.Context(), query, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('legacy-gate','Legacy gate') RETURNING id")
	f.actor = scalar("INSERT INTO users(email,password_hash) VALUES('gate-operator@example.test','fixture') RETURNING id")
	if _, err = f.p.Exec(t.Context(), "INSERT INTO platform_admins(user_id) VALUES($1)", f.actor); err != nil {
		t.Fatal(err)
	}
	f.tenant = scalar("INSERT INTO tenants(brand_id,name,slug,status,owner_user_id) VALUES($1,'Legacy gate','legacy-gate','active',$2) RETURNING id", brand, f.actor)
	if _, err = f.p.Exec(t.Context(), "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", f.actor, f.tenant); err != nil {
		t.Fatal(err)
	}
	q := store.New(f.p.Pool)
	if grant {
		if _, err = q.PlatformGrantMigrationEntitlement(t.Context(), store.PlatformGrantMigrationEntitlementParams{TenantID: f.tenant, ActorID: f.actor, ManifestSha256: strings.Repeat("a", 64), Reason: "Reviewed source and explicitly authorized isolated migration"}); err != nil {
			t.Fatal(err)
		}
	}
	f.domain, err = q.PlatformCreateLegacyDomain(t.Context(), store.PlatformCreateLegacyDomainParams{TenantID: f.tenant, Fqdn: "legacy-gate.example.test", VerificationToken: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func legacyGateInsert(ctx context.Context, q *store.Queries, f legacyGateFixture, local string) error {
	_, err := q.CreateMailbox(ctx, store.CreateMailboxParams{TenantID: f.tenant, DomainID: f.domain, LocalPart: local, Address: local + "@legacy-gate.example.test", PasswordHash: "fixture", QuotaBytes: 1024, Status: "active", SendEnabled: true})
	return err
}

func legacyGateSQLState(t *testing.T, err error, code, message string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != code || !strings.Contains(pg.Message, message) {
		t.Fatalf("expected controlled SQLSTATE %s (%s), got %v", code, message, err)
	}
}

func TestLegacyMetadataGateIndependentTrustedOnlyAndNoActivation(t *testing.T) {
	f := newLegacyGateFixture(t, true)
	ctx := t.Context()
	root, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close(context.Background()) }()
	tx, err := root.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SET LOCAL ROLE urmail_app"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SELECT set_config('urmail.tenant_id',$1,true),set_config('urmail.user_id',$2,true),set_config('urmail.legacy_import','true',true),set_config('urmail.migration_operator','true',true)", m8ID(f.tenant), m8ID(f.actor)); err != nil {
		t.Fatal(err)
	}
	var role string
	var visible int
	if err = tx.QueryRow(ctx, "SELECT current_user,(SELECT count(*) FROM domains WHERE tenant_id=$1 AND id=$2)", f.tenant, f.domain).Scan(&role, &visible); err != nil || role != "urmail_app" || visible != 1 {
		t.Fatalf("invalid scoped app fixture role=%s rows=%d err=%v", role, visible, err)
	}
	legacyGateSQLState(t, legacyGateInsert(ctx, store.New(tx), f, "app"), "23514", "DOMAIN_NOT_PROVISIONED")
	_ = tx.Rollback(ctx)
	q := store.New(f.p.Pool)
	if err = legacyGateInsert(ctx, q, f, "trusted"); err != nil {
		t.Fatal("reviewed platform metadata denied", err)
	}
	// The direct privileged session fallback is deliberate, not inherited authority
	// available after that same session explicitly changes to the app role.
	if err = legacyGateInsert(ctx, store.New(root), f, "direct"); err != nil {
		t.Fatal(err)
	}
	// The migration role is also explicitly trusted for staging, without any
	// app-role inheritance fallback. Roll back this extra allocation.
	migration, err := root.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = migration.Exec(ctx, "SET LOCAL ROLE urmail_migrate"); err != nil {
		t.Fatal(err)
	}
	if err = legacyGateInsert(ctx, store.New(migration), f, "migrator"); err != nil {
		t.Fatal("trusted migrator metadata denied", err)
	}
	if err = migration.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var ready bool
	if err = f.p.QueryRow(ctx, "SELECT urmail_domain_mail_ready($1,$2)", f.tenant, f.domain).Scan(&ready); err != nil || ready {
		t.Fatalf("staging enabled domain ready=%t err=%v", ready, err)
	}
	rows, err := q.PlatformListMailboxes(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatalf("pending metadata entered runtime map rows=%d err=%v", len(rows), err)
	}
	_, err = q.PlatformCreateMailboxSession(ctx, store.PlatformCreateMailboxSessionParams{TokenHash: strings.Repeat("c", 64), Address: "trusted@legacy-gate.example.test"})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("pending domain obtained authenticated mailbox session: %v", err)
	}
	m, err := db.NewMigrator(f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	legacyGateNormalize70(t, m)
	if err = m.Down(ctx); err == nil || !strings.Contains(err.Error(), "LEGACY_PENDING_MAILBOXES_REQUIRE_CONTROLLED_ROLLBACK") {
		t.Fatalf("dependent pending rows lost rollback gate: %v", err)
	}
	var n int
	if err = f.p.QueryRow(ctx, "SELECT count(*) FROM mailboxes WHERE tenant_id=$1", f.tenant).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rejected rollback changed metadata rows=%d err=%v", n, err)
	}
}

func TestLegacyMetadataGateIndependentNoGrantAndReversibleWithoutDependency(t *testing.T) {
	f := newLegacyGateFixture(t, false)
	legacyGateSQLState(t, legacyGateInsert(t.Context(), store.New(f.p.Pool), f, "operator"), "23514", "DOMAIN_NOT_PROVISIONED")
	m, err := db.NewMigrator(f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	legacyGateNormalize70(t, m)
	if err = m.Down(t.Context()); err != nil {
		t.Fatal(err)
	}
	if v, dirty, e := m.Version(); e != nil || dirty || v != 69 {
		t.Fatalf("down70 failed v=%d dirty=%t err=%v", v, dirty, e)
	}
	if err = m.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	legacyGateSQLState(t, legacyGateInsert(t.Context(), store.New(f.p.Pool), f, "still-denied"), "23514", "DOMAIN_NOT_PROVISIONED")
}

func TestLegacyMetadataGateIndependentAuthorityRevocationSerialization(t *testing.T) {
	f := newLegacyGateFixture(t, false)
	ctx := t.Context()
	importer, err := f.p.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = importer.Rollback(ctx) }()
	arg := store.PlatformLockLegacyImportTargetParams{ActorID: f.actor, TenantID: f.tenant}
	if _, err = store.New(importer).PlatformLockLegacyImportTarget(ctx, arg); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"DELETE FROM platform_admins WHERE user_id=$1", "UPDATE users SET status='disabled' WHERE id=$1"} {
		revoked, e := f.p.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = revoked.Exec(ctx, "SET LOCAL lock_timeout='100ms'"); e != nil {
			t.Fatal(e)
		}
		_, e = revoked.Exec(ctx, query, f.actor)
		legacyGateSQLState(t, e, "55P03", "")
		_ = revoked.Rollback(ctx)
	}
	if err = importer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.p.Exec(ctx, "DELETE FROM platform_admins WHERE user_id=$1", f.actor); err != nil {
		t.Fatal(err)
	}
	assertDenied := func() {
		t.Helper()
		tx, e := f.p.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		_, e = store.New(tx).PlatformLockLegacyImportTarget(ctx, arg)
		if !errors.Is(e, pgx.ErrNoRows) {
			t.Fatalf("revoked authority accepted: %v", e)
		}
	}
	assertDenied()
	if _, err = f.p.Exec(ctx, "INSERT INTO platform_admins(user_id) VALUES($1)", f.actor); err != nil {
		t.Fatal(err)
	}
	if _, err = f.p.Exec(ctx, "UPDATE users SET status='disabled' WHERE id=$1", f.actor); err != nil {
		t.Fatal(err)
	}
	assertDenied()
}

func legacyGateNormalize70(t *testing.T, m *db.Migrator) {
	t.Helper()
	for {
		version, dirty, err := m.Version()
		if err != nil || dirty || version < 70 {
			t.Fatalf("invalid gate fixture v=%d dirty=%t err=%v", version, dirty, err)
		}
		if version == 70 {
			return
		}
		if err = m.Down(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
