package db_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestMigrationEntitlementAuthorityAndReplay(t *testing.T) {
	dsn := testDatabaseURL(t)
	applyMigrations(t, dsn)
	p, err := db.OpenPlatform(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	q := store.New(p.Pool)
	var brand, actor, tenant int64
	if err = p.QueryRow(t.Context(), "INSERT INTO brands(slug,name) VALUES('migration-test','Migration') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(t.Context(), "INSERT INTO users(email,password_hash) VALUES('migration-operator@example.test','unused') RETURNING id").Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(t.Context(), "INSERT INTO platform_admins(user_id) VALUES($1)", actor); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(t.Context(), "INSERT INTO tenants(brand_id,name,slug,status,owner_user_id) VALUES($1,'Migration','migration-test','active',$2) RETURNING id", brand, actor).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(t.Context(), "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", actor, tenant); err != nil {
		t.Fatal(err)
	}
	manifest := strings.Repeat("a", 64)
	grant := func(tid int64, digest string) (store.Subscriptions, error) {
		return q.PlatformGrantMigrationEntitlement(t.Context(), store.PlatformGrantMigrationEntitlementParams{TenantID: tid, ManifestSha256: digest, ActorID: actor, Reason: "Reviewed source and explicit preserved service mapping"})
	}
	sub, err := grant(tenant, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if sub.EntitlementSource != "migration_legacy" || sub.PeriodEnd.Valid || sub.MigrationManifestSha256.String != manifest {
		t.Fatalf("invented term: %+v", sub)
	}
	var allowed, review bool
	var limit int64
	if err = p.QueryRow(t.Context(), "SELECT urmail_mail_allowed($1),urmail_billing_history_requires_review($1,0),(SELECT quota_bytes FROM urmail_effective_plan($1))", tenant).Scan(&allowed, &review, &limit); err != nil || !allowed || review || limit != -1 {
		t.Fatalf("authority mismatch mail=%v review=%v limit=%d err=%v", allowed, review, limit, err)
	}
	var facts int
	if err = p.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM orders WHERE tenant_id=$1)+(SELECT count(*) FROM payments WHERE tenant_id=$1)+(SELECT count(*) FROM invoices WHERE tenant_id=$1)+(SELECT count(*) FROM subscription_terms WHERE tenant_id=$1)+(SELECT count(*) FROM billing_calendars WHERE tenant_id=$1)+(SELECT count(*) FROM tenant_trials WHERE tenant_id=$1)", tenant).Scan(&facts); err != nil || facts != 0 {
		t.Fatalf("invented financial facts %d %v", facts, err)
	}
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), "SET LOCAL ROLE urmail_app"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), "SELECT set_config('urmail.tenant_id',$1,true),set_config('urmail.user_id',$2,true)", m8ID(tenant), m8ID(actor)); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(t.Context(), "SELECT urmail_migration_entitlement_allowed($1)", tenant).Scan(&allowed); err != nil || !allowed {
		t.Fatal("legitimate scoped migration unavailable", err)
	}
	if err = tx.QueryRow(t.Context(), "SELECT urmail_migration_entitlement_allowed($1)", tenant+1000).Scan(&allowed); err != nil || allowed {
		t.Fatal("migration helper leaked across tenant context", err)
	}
	for i := 0; i < 7; i++ {
		if _, err = tx.Exec(t.Context(), "INSERT INTO domains(tenant_id,fqdn,verification_token) VALUES($1,$2,$3)", tenant, "migration-domain-"+m8ID(int64(i))+".example.test", strings.Repeat("a", 63)+m8ID(int64(i))); err != nil {
			t.Fatal("unlimited domain allocation rejected", err)
		}
	}
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"UPDATE subscriptions SET cancel_at_period_end=true WHERE tenant_id=$1", "UPDATE subscriptions SET migration_manifest_sha256='forged' WHERE tenant_id=$1", "SELECT urmail_grant_migration_entitlement($1,'" + strings.Repeat("b", 64) + "'," + m8ID(actor) + ",'forged app migration service')"} {
		tx, err = p.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func(current pgx.Tx) { _ = current.Rollback(t.Context()) }(tx)
		if _, err = tx.Exec(t.Context(), "SET LOCAL ROLE urmail_app"); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(t.Context(), "SELECT set_config('urmail.tenant_id',$1,true),set_config('urmail.user_id',$2,true)", m8ID(tenant), m8ID(actor)); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(t.Context(), statement, tenant); err == nil {
			t.Fatal("application forged or canceled a preserved grant")
		}
		_ = tx.Rollback(t.Context())
	}
	if _, err = p.Exec(t.Context(), "UPDATE plans SET active=true WHERE code='migration_legacy'"); err == nil {
		t.Fatal("hidden migration plan became saleable")
	}
	if _, err = p.Exec(t.Context(), "UPDATE subscriptions SET status='canceled' WHERE id=$1", sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(t.Context(), "INSERT INTO subscriptions(tenant_id,plan_id,status,period_start,period_end) VALUES($1,'free','active',now(),now()+interval '1 month')", tenant); err != nil {
		t.Fatal(err)
	}
	replay, err := grant(tenant, manifest)
	if err != nil || replay.ID != sub.ID || replay.Status != "canceled" {
		t.Fatal("replay resurrected canceled migration", err)
	}
	if _, err = grant(tenant, strings.Repeat("c", 64)); err == nil {
		t.Fatal("new manifest silently regranted legacy service")
	}
	migrator, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrator.Close() }()
	for {
		version, dirty, versionErr := migrator.Version()
		if versionErr != nil || dirty || version < 66 {
			t.Fatalf("cannot reach clean schema66: %d %t %v", version, dirty, versionErr)
		}
		if version == 66 {
			break
		}
		if err = migrator.Down(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err = migrator.Down(t.Context()); err == nil || !strings.Contains(err.Error(), "MIGRATION_ENTITLEMENTS_REQUIRE_CONTROLLED_ROLLBACK") {
		t.Fatalf("rollback forgot provenance: %v", err)
	}
}

func TestMigrationEntitlementAuditFailureIsAtomic(t *testing.T) {
	dsn := testDatabaseURL(t)
	applyMigrations(t, dsn)
	p, err := db.OpenPlatform(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var actor, brand, tenant int64
	if err = p.QueryRow(t.Context(), "INSERT INTO users(email,password_hash) VALUES('migration-audit@example.test','unused') RETURNING id").Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(t.Context(), "INSERT INTO platform_admins(user_id) VALUES($1)", actor); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(t.Context(), "INSERT INTO brands(slug,name) VALUES('migration-audit','Migration') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(t.Context(), "INSERT INTO tenants(brand_id,name,slug) VALUES($1,'Migration','migration-audit') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(t.Context(), "UPDATE subscriptions SET status='active',period_end=now()+interval '1 month' WHERE tenant_id=$1", tenant); err != nil {
		t.Fatal(err)
	}
	_, err = p.Exec(t.Context(), `CREATE FUNCTION reject_migration_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='GRANT migration.entitlement' THEN RAISE EXCEPTION 'TEST_AUDIT_UNAVAILABLE'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_migration_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_migration_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.New(p.Pool).PlatformGrantMigrationEntitlement(t.Context(), store.PlatformGrantMigrationEntitlementParams{TenantID: tenant, ManifestSha256: strings.Repeat("d", 64), ActorID: actor, Reason: "Reviewed source and explicit preserved service mapping"})
	if err == nil || !strings.Contains(err.Error(), "TEST_AUDIT_UNAVAILABLE") {
		t.Fatal("audit failure was ignored", err)
	}
	var retained int
	if err = p.QueryRow(t.Context(), "SELECT count(*) FROM subscriptions WHERE tenant_id=$1 AND plan_id='free' AND status='active'", tenant).Scan(&retained); err != nil || retained != 1 {
		t.Fatal("failed grant canceled prior rights", err)
	}
	var migration int
	if err = p.QueryRow(t.Context(), "SELECT count(*) FROM subscriptions WHERE tenant_id=$1 AND entitlement_source='migration_legacy'", tenant).Scan(&migration); err != nil || migration != 0 {
		t.Fatal("failed audit left a grant", err)
	}
	if _, err = p.Exec(t.Context(), "DROP TRIGGER reject_migration_audit ON audit_logs; DROP FUNCTION reject_migration_audit()"); err != nil {
		t.Fatal(err)
	}
	_, err = store.New(p.Pool).PlatformGrantMigrationEntitlement(t.Context(), store.PlatformGrantMigrationEntitlementParams{TenantID: tenant, ManifestSha256: strings.Repeat("d", 64), ActorID: actor, Reason: "Reviewed source and explicit preserved service mapping"})
	if err != nil {
		t.Fatal("safe retry did not recover", err)
	}
}

func m8ID(value int64) string { return strconv.FormatInt(value, 10) }
