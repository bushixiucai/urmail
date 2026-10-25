package db_test

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func seedRestoreExternalWork(t *testing.T, dsn string, latest bool) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatal(err)
	}
	query := `DO $$ DECLARE b bigint;t bigint;o bigint;p bigint; BEGIN
 INSERT INTO brands(slug,name) VALUES('restore-replay','Restore') RETURNING id INTO b;
 INSERT INTO tenants(brand_id,name,slug,status) VALUES(b,'Restore','restore-replay','active') RETURNING id INTO t;
 INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency) VALUES(t,'restore-order','new','paid',1000,'CNY') RETURNING id INTO o;
 INSERT INTO payments(tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status) VALUES(t,o,'manual','stable-payment',1000,'CNY','succeeded') RETURNING id INTO p;
 INSERT INTO refunds(tenant_id,order_id,payment_id,refund_no,amount_cents,currency,status) VALUES(t,o,p,'stable-refund',100,'CNY','pending');
 INSERT INTO onboarding_tasks(tenant_id,order_id,status,leased_by,leased_until) VALUES(t,o,'running','old-source',now()+interval '1 hour');
 END $$;`
	if _, err = conn.Exec(ctx, query); err != nil {
		t.Fatal(err)
	}
	if !latest {
		return
	}
	query = `DO $$ DECLARE t bigint;o bigint;d bigint;m bigint;c bigint;tpl bigint;l bigint;camp bigint;w bigint;u bigint; BEGIN
 SELECT id INTO t FROM tenants WHERE slug='restore-replay'; SELECT id INTO o FROM orders WHERE order_no='restore-order';
 INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES(t,'restore.example.test','active','fixture',now()) RETURNING id INTO d;
 INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES(t,d,'alice','alice@restore.example.test','unused',1024) RETURNING id INTO m;
 INSERT INTO marketing_channels(tenant_id,name,kind,sender_address,encrypted_config) VALUES(t,'restore','dedicated_gateway','alice@restore.example.test','enc:v1:fixture') RETURNING id INTO c;
 INSERT INTO marketing_templates(tenant_id,name,subject,text_body) VALUES(t,'restore','subject','body') RETURNING id INTO tpl;
 INSERT INTO marketing_lists(tenant_id,name) VALUES(t,'restore') RETURNING id INTO l;
 INSERT INTO marketing_campaigns(tenant_id,name,channel_id,template_id,list_id,status) VALUES(t,'restore',c,tpl,l,'running') RETURNING id INTO camp;
 INSERT INTO marketing_quota_windows(tenant_id,starts_at,ends_at,timezone,used_count) VALUES(t,now()-interval '1 hour',now()+interval '23 hours','Asia/Shanghai',3) RETURNING id INTO w;
 INSERT INTO marketing_recipients(tenant_id,campaign_id,email,status,lease_owner,lease_until,fence,window_id,reserved) VALUES(t,camp,'queued@example.test','queued',NULL,NULL,1,w,true),(t,camp,'leased@example.test','leased','old-source',now()+interval '1 hour',3,w,true),(t,camp,'sent@example.test','sent',NULL,NULL,5,w,true);
 INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,provider,model,cost_currency,input_rate,output_rate,reserved_tokens,reserved_cost_micros) VALUES(t,m,'stable-ai-request',repeat('a',64),now()-interval '1 hour',now()+interval '1 hour','platform','openai','fixture','CNY',1,1,100,75);
 INSERT INTO payment_checkout_intents(order_id,tenant_id,provider,account_scope,session_id,state) VALUES(o,t,'manual','stable-account','stable-session','ready');
 INSERT INTO notification_outbox(tenant_id,kind,message_key,dedupe_key,channel,recipient,recipient_verified) VALUES(t,'restore','fixture','stable-notification','email','verified@example.test',true);
 INSERT INTO notification_outbox(tenant_id,kind,message_key,dedupe_key,channel) VALUES(t,'restore','fixture','stable-inapp','in_app');
 INSERT INTO users(email,password_hash) VALUES('restore-user@example.test','unused') RETURNING id INTO u;
 INSERT INTO sessions(id,user_id,tenant_id,csrf_token,expires_at) VALUES('restore-session',u,t,'fixture',now()+interval '1 day');
 INSERT INTO platform_admins(user_id) VALUES(u);
 INSERT INTO refunds(tenant_id,order_id,payment_id,refund_no,amount_cents,currency,status,approved_by,approval_reason,provider_refund_no) SELECT t,o,id,'approved-stable-refund',200,'CNY','approved',u,'Approved before snapshot','upstream-stable-refund' FROM payments WHERE order_id=o;
 INSERT INTO refunds(tenant_id,order_id,payment_id,refund_no,amount_cents,currency,status,approved_by,approval_reason) SELECT t,o,id,'completed-stable-refund',300,'CNY','completed',u,'Completed before snapshot' FROM payments WHERE order_id=o;
 END $$;`
	if _, err = conn.Exec(ctx, query); err != nil {
		t.Fatal(err)
	}
	// Recreate the retired Roundcube boundary so restore quarantine still
	// exercises invalidation of legacy encrypted session cookies.
	if _, err = conn.Exec(ctx, `RESET ROLE;
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_roundcube') THEN
  CREATE ROLE urmail_roundcube NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
 END IF;
 IF NOT pg_has_role('urmail_migrate','urmail_roundcube','MEMBER') THEN
  GRANT urmail_roundcube TO urmail_migrate;
 END IF;
 EXECUTE 'ALTER ROLE urmail_roundcube SET search_path=roundcube';
END $$;
CREATE SCHEMA IF NOT EXISTS roundcube AUTHORIZATION urmail_roundcube;
CREATE TABLE roundcube.session(sess_id text PRIMARY KEY,changed timestamptz NOT NULL,vars text NOT NULL);
ALTER TABLE roundcube.session OWNER TO urmail_roundcube;
INSERT INTO roundcube.session VALUES('old-roundcube-cookie',now(),'restored-encrypted-imap-password');
CREATE FUNCTION roundcube.session_owner_probe() RETURNS name LANGUAGE SQL SECURITY DEFINER AS 'SELECT current_user';
ALTER FUNCTION roundcube.session_owner_probe() OWNER TO urmail_roundcube`); err != nil {
		t.Fatal(err)
	}
}

func assertRestoredRefundHeld(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var state, no, code string
	var approved *int64
	var reason *string
	var amount int64
	err = conn.QueryRow(ctx, `SELECT status,refund_no,last_error_code,approved_by,approval_reason,amount_cents FROM refunds WHERE refund_no='stable-refund'`).Scan(&state, &no, &code, &approved, &reason, &amount)
	if err != nil || state != "uncertain" || no != "stable-refund" || amount != 100 || approved != nil || reason != nil || !strings.Contains(code, "RESTORE_RECONCILIATION_REQUIRED") {
		t.Fatalf("refund fact was lost or replayable: state=%s no=%s amount=%d approved=%v reason=%v code=%s err=%v", state, no, amount, approved, reason, code, err)
	}
	var enabled string
	if err = conn.QueryRow(ctx, `SELECT tgenabled::text FROM pg_trigger WHERE tgname='refund_approval_guard'`).Scan(&enabled); err != nil || enabled != "O" {
		t.Fatalf("approval guard not restored: %s %v", enabled, err)
	}
	var task string
	var owner *string
	err = conn.QueryRow(ctx, `SELECT status,leased_by FROM onboarding_tasks WHERE order_id=(SELECT id FROM orders WHERE order_no='restore-order')`).Scan(&task, &owner)
	if err != nil || task != "blocked" || owner != nil {
		t.Fatalf("onboarding replayable: %s %v %v", task, owner, err)
	}
}

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

func nativeToolDB(t *testing.T, program, dsn string, args ...string) *exec.Cmd {
	t.Helper()
	uri, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	password := ""
	if uri.User != nil {
		password, _ = uri.User.Password()
		uri.User = url.User(uri.User.Username())
	}
	query := uri.Query()
	query.Del("role")
	query.Del("password")
	uri.RawQuery = query.Encode()
	command := exec.CommandContext(t.Context(), program, append(args, "--dbname", uri.String())...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PGPASSWORD=") {
			command.Env = append(command.Env, entry)
		}
	}
	if password != "" {
		command.Env = append(command.Env, "PGPASSWORD="+password)
	}
	return command
}
func nativeRestoreOwned(t *testing.T, source, target string) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "owned-snapshot.dump")
	if out, err := nativeToolDB(t, "pg_dump", source, "--format=custom", "--file", archive).CombinedOutput(); err != nil {
		t.Fatalf("dump owned fixture: %v %s", err, out)
	}
	toc, err := exec.CommandContext(t.Context(), "pg_restore", "--list", archive).Output()
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := db.FilterRestoreTOC(string(toc))
	if err != nil {
		t.Fatal(err)
	}
	listFile := filepath.Join(t.TempDir(), "restore.list")
	if err = os.WriteFile(listFile, []byte(filtered), 0600); err != nil {
		t.Fatal(err)
	}
	sql, err := exec.CommandContext(t.Context(), "pg_restore", "--no-owner", "--use-list", listFile, "--file=-", archive).Output()
	if err != nil {
		t.Fatal(err)
	}
	sqlFile := filepath.Join(t.TempDir(), "restore.sql")
	if err = os.WriteFile(sqlFile, append(append([]byte(db.RestoreSQLPrefix), sql...), []byte("\nCOMMIT;\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := nativeToolDB(t, "psql", target, "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--file", sqlFile).CombinedOutput(); err != nil {
		t.Fatalf("restore with NOSUPERUSER migration role: %v %s", err, out)
	}
	migrationURL, _ := url.Parse(target)
	query := migrationURL.Query()
	query.Set("role", "urmail_migrate")
	migrationURL.RawQuery = query.Encode()
	return migrationURL.String()

}
