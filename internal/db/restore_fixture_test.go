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
	query := `DO $$ DECLARE b bigint;t bigint; BEGIN
 INSERT INTO brands(slug,name) VALUES('restore-replay','Restore') RETURNING id INTO b;
 INSERT INTO tenants(brand_id,name,slug,status) VALUES(b,'Restore','restore-replay','active') RETURNING id INTO t;
 INSERT INTO onboarding_tasks(tenant_id,status,leased_by,leased_until) VALUES(t,'running','old-source',now()+interval '1 hour');
 END $$;`
	if _, err = conn.Exec(ctx, query); err != nil {
		t.Fatal(err)
	}
	if !latest {
		return
	}
	query = `DO $$ DECLARE t bigint;d bigint;m bigint;c bigint;tpl bigint;l bigint;camp bigint;w bigint;u bigint; BEGIN
 SELECT id INTO t FROM tenants WHERE slug='restore-replay';
 INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES(t,'restore.example.test','active','fixture',now()) RETURNING id INTO d;
 INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES(t,d,'alice','alice@restore.example.test','unused',1024) RETURNING id INTO m;
 INSERT INTO mailbox_scheduled_sends(tenant_id,mailbox_id,draft_folder,draft_uid,draft_uidvalidity,send_at,status,smtp_secret,claim_token,lease_until)
 SELECT t,m,'Drafts',n,9,now()-interval '1 hour',state,'enc:fixture',4,now()+interval '1 hour'
 FROM (VALUES(1,'pending'),(2,'preparing'),(3,'submitting'),(4,'uncertain'),(5,'sent'),(6,'cancelled')) v(n,state);
 INSERT INTO marketing_channels(tenant_id,name,kind,sender_address,encrypted_config) VALUES(t,'restore','dedicated_gateway','alice@restore.example.test','enc:v1:fixture') RETURNING id INTO c;
 INSERT INTO marketing_templates(tenant_id,name,subject,text_body) VALUES(t,'restore','subject','body') RETURNING id INTO tpl;
 INSERT INTO marketing_lists(tenant_id,name) VALUES(t,'restore') RETURNING id INTO l;
 INSERT INTO marketing_campaigns(tenant_id,name,channel_id,template_id,list_id,status) VALUES(t,'restore',c,tpl,l,'running') RETURNING id INTO camp;
 INSERT INTO marketing_quota_windows(tenant_id,starts_at,ends_at,timezone,used_count) VALUES(t,now()-interval '1 hour',now()+interval '23 hours','Asia/Shanghai',3) RETURNING id INTO w;
 INSERT INTO marketing_recipients(tenant_id,campaign_id,email,status,lease_owner,lease_until,fence,window_id,reserved) VALUES(t,camp,'queued@example.test','queued',NULL,NULL,1,w,true),(t,camp,'leased@example.test','leased','old-source',now()+interval '1 hour',3,w,true),(t,camp,'sent@example.test','sent',NULL,NULL,5,w,true);
 INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,provider,model,cost_currency,input_rate,output_rate,reserved_tokens,reserved_cost_micros) VALUES(t,m,'stable-ai-request',repeat('a',64),now()-interval '1 hour',now()+interval '1 hour','platform','openai','fixture','CNY',1,1,100,75);
 INSERT INTO notification_outbox(tenant_id,kind,message_key,dedupe_key,channel,recipient,recipient_verified) VALUES(t,'restore','fixture','stable-notification','email','verified@example.test',true);
 INSERT INTO notification_outbox(tenant_id,kind,message_key,dedupe_key,channel) VALUES(t,'restore','fixture','stable-inapp','in_app');
 INSERT INTO users(email,password_hash) VALUES('restore-user@example.test','unused') RETURNING id INTO u;
 INSERT INTO sessions(id,user_id,tenant_id,csrf_token,expires_at) VALUES('restore-session',u,t,'fixture',now()+interval '1 day');
 INSERT INTO platform_admins(user_id) VALUES(u);
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
func assertRestoredRefundHeld(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var task string
	var owner *string
	err = conn.QueryRow(ctx, `SELECT ot.status,ot.leased_by FROM onboarding_tasks ot JOIN tenants tn ON tn.id=ot.tenant_id WHERE tn.slug='restore-replay'`).Scan(&task, &owner)
	if err != nil || task != "blocked" || owner != nil {
		t.Fatalf("onboarding replayable: %s %v %v", task, owner, err)
	}
}
