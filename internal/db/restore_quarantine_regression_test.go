package db_test

// Regression coverage for the restore quarantine and side-effect hold path.
// These tests were carried by internal/db/restore_quarantine_test.go before the
// helpers were split out; the behavioral assertions must not be dropped.

import (
	"context"
	"net/url"
	"os/exec"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestLegacyRestoreHoldsPendingRefundAfterSchemaUpgrade(t *testing.T) {
	dsn, connection := legacyDomainDatabase(t)
	seedRestoreExternalWork(t, dsn, false)
	if err := db.FinalizeRestore(context.Background(), dsn, "legacy-snapshot"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := connection.QueryRowContext(context.Background(), "SELECT status FROM refunds WHERE refund_no='stable-refund'").Scan(&status); err != nil || status != "pending" {
		t.Fatalf("legacy state was fabricated: %s %v", status, err)
	}
	migration, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer migration.Close()
	if err = migration.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = db.HoldRestoredSideEffects(context.Background(), dsn, "legacy-snapshot"); err != nil {
		t.Fatal(err)
	}
	assertRestoredRefundHeld(t, dsn)
}

func TestNativeDatabaseRestoreQuarantinesExternalReplay(t *testing.T) {
	for _, binary := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip("native PostgreSQL tools unavailable")
		}
	}
	source := testdb.New(t)
	roleURL := func(dsn string) string {
		parsed, _ := url.Parse(dsn)
		params := parsed.Query()
		params.Set("role", "urmail_migrate")
		parsed.RawQuery = params.Encode()
		return parsed.String()
	}
	migration, err := db.NewMigrator(roleURL(source))
	if err != nil {
		t.Fatal(err)
	}
	if err = migration.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	migration.Close()
	seedRestoreExternalWork(t, source, true)
	target := testdb.New(t)

	// An already-initialized newer target must be replaced atomically too.
	targetMigration, err := db.NewMigrator(roleURL(target))
	if err != nil {
		t.Fatal(err)
	}
	if err = targetMigration.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	targetMigration.Close()
	maintenanceURL := nativeRestoreOwned(t, source, target)

	if err = db.FinalizeRestore(context.Background(), maintenanceURL, "native-snapshot"); err != nil {
		t.Fatal(err)
	}
	if err = db.HoldRestoredSideEffects(context.Background(), maintenanceURL, "native-snapshot"); err != nil {
		t.Fatal(err)
	}
	assertRestoredRefundHeld(t, target)
	if err = db.VerifyRuntimePrivileges(context.Background(), maintenanceURL); err != nil {
		t.Fatal(err)
	}
	if err = db.VerifyRestoreIdentity(context.Background(), target, "native-snapshot"); err != nil {
		t.Fatal(err)
	}
	if err = db.VerifyRestoreIdentity(context.Background(), target, "another-restore"); err == nil {
		t.Fatal("release matched another restore")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	checks := []struct{ name, query string }{
		{"scheduled external replay remains held", `SELECT count(*)=4 AND bool_and(status='held' AND smtp_secret='' AND lease_until IS NULL AND claim_token>=5) FROM mailbox_scheduled_sends WHERE draft_uid<=4`},
		{"scheduled completed history retained", `SELECT count(*)=2 AND bool_and(status IN('sent','cancelled')) FROM mailbox_scheduled_sends WHERE draft_uid>4`},
		{"approved refund fact retained", `SELECT status='uncertain' AND approved_by IS NOT NULL AND approval_reason='Approved before snapshot' AND amount_cents=200 AND provider_refund_no='upstream-stable-refund' FROM refunds WHERE refund_no='approved-stable-refund'`},
		{"completed refund untouched", `SELECT status='completed' AND amount_cents=300 FROM refunds WHERE refund_no='completed-stable-refund'`},
		{"marketing retains quota and stable recipient but invalidates lease", `SELECT count(*)=2 AND bool_and(status='uncertain' AND reserved AND lease_owner IS NULL AND lease_until IS NULL AND fence>=3) FROM marketing_recipients WHERE email<>'sent@example.test'`},
		{"completed marketing untouched", `SELECT status='sent' AND fence=5 FROM marketing_recipients WHERE email='sent@example.test'`},
		{"campaign paused", `SELECT status='paused' FROM marketing_campaigns`},
		{"quota not released", `SELECT used_count=3 FROM marketing_quota_windows`},
		{"AI budget retained", `SELECT status='uncertain' AND reserved_tokens=100 AND reserved_cost_micros=75 AND request_key='stable-ai-request' FROM ai_usage_requests`},
		{"checkout uncertain with stable upstream identity", `SELECT state='uncertain' AND restore_hold AND session_id='stable-session' FROM payment_checkout_intents`},
		{"email uncertain", `SELECT state='uncertain' AND lease_until IS NULL AND fence=2 FROM notification_outbox WHERE channel='email'`},
		{"in app not held", `SELECT state='pending' AND fence=0 FROM notification_outbox WHERE channel='in_app'`},
		{"Roundcube SECURITY DEFINER owner stays limited", `SELECT roundcube.session_owner_probe()='urmail_roundcube'`},
		{"Roundcube dedicated owner restored", `SELECT relowner='urmail_roundcube'::regrole AND NOT has_table_privilege('urmail_app','roundcube.session','SELECT') FROM pg_class WHERE oid='roundcube.session'::regclass`},
		{"Roundcube cookie credentials invalidated", `SELECT count(*)=0 FROM roundcube.session`},
		{"Nextcloud schema must not exist", `SELECT NOT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname='nextcloud')`},
		// Role may be absent after 0073, or retained cluster-wide when another
		// database still depends on it; either way this database must be clean.
		{"Nextcloud role owns nothing in this database", `SELECT NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_nextcloud') OR (
 NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_roles o ON o.oid=c.relowner WHERE o.rolname='urmail_nextcloud')
 AND NOT EXISTS(SELECT 1 FROM pg_proc p JOIN pg_roles o ON o.oid=p.proowner WHERE o.rolname='urmail_nextcloud')
 AND NOT EXISTS(SELECT 1 FROM pg_namespace n JOIN pg_roles o ON o.oid=n.nspowner WHERE o.rolname='urmail_nextcloud')
 AND NOT EXISTS(SELECT 1 FROM pg_type t JOIN pg_roles o ON o.oid=t.typowner WHERE o.rolname='urmail_nextcloud')
 AND NOT EXISTS(SELECT 1 FROM pg_default_acl d JOIN pg_roles o ON o.oid=d.defaclrole WHERE o.rolname='urmail_nextcloud')
)`},
		{"source auth expired", `SELECT expires_at<=now() FROM sessions WHERE id='restore-session'`},
		{"original refund provenance retained", `SELECT EXISTS(SELECT 1 FROM audit_logs WHERE action='restore.external_work_held' AND detail->>'source_table'='refunds' AND detail->>'previous_state'='pending')`},
		{"restore alert persists", `SELECT state='firing' FROM operational_alerts WHERE alert_key='restore:external_reconciliation'`},
	}
	sourceConn, err := pgx.Connect(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceConn.Close(ctx)
	var sourceSessions int
	if err = sourceConn.QueryRow(ctx, "SELECT count(*) FROM roundcube.session").Scan(&sourceSessions); err != nil || sourceSessions != 1 {
		t.Fatalf("native restore modified source authentication: %d %v", sourceSessions, err)
	}
	for _, check := range checks {
		var ok bool
		if err = conn.QueryRow(ctx, check.query).Scan(&ok); err != nil || !ok {
			t.Errorf("%s: %v (%v)", check.name, ok, err)
		}
	}
}

func TestRestoreHoldFailureRollsBackClassificationAndApprovalGuard(t *testing.T) {
	dsn := testdb.New(t)
	ctx := context.Background()
	migration, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer migration.Close()
	if err = migration.Up(ctx); err != nil {
		t.Fatal(err)
	}
	seedRestoreExternalWork(t, dsn, true)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, `CREATE FUNCTION fail_restore_hold() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected_restore_hold_failure'; END $$; CREATE TRIGGER fail_restore_hold BEFORE INSERT OR UPDATE ON settings FOR EACH ROW EXECUTE FUNCTION fail_restore_hold()`); err != nil {
		t.Fatal(err)
	}
	if err = db.HoldRestoredSideEffects(ctx, dsn, "failure-snapshot"); err == nil {
		t.Fatal("classification failure was ignored")
	}
	var ok bool
	if err = conn.QueryRow(ctx, `SELECT (SELECT status='pending' AND approved_by IS NULL FROM refunds WHERE refund_no='stable-refund') AND (SELECT tgenabled='O' FROM pg_trigger WHERE tgname='refund_approval_guard') AND (SELECT status='reserved' FROM ai_usage_requests) AND (SELECT status='queued' AND fence=1 FROM marketing_recipients WHERE email='queued@example.test') AND (SELECT count(*)=1 FROM roundcube.session)`).Scan(&ok); err != nil || !ok {
		t.Fatalf("partial classification or disabled guard survived: %v %v", ok, err)
	}
}
