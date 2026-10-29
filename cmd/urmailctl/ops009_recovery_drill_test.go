package main

// OPS-009 recovery/upgrade drill: after EVERY persisted model and external
// side effect exists (schema head 0130), one fact per domain is seeded into a
// fresh database, dumped with real pg_dump and restored into a SECOND fresh
// database through the production restore machinery (TOC filtering, atomic
// SQL restore as the NOSUPERUSER migration role, ownership finalization,
// side-effect holds, runtime privilege verification, quarantine isolation and
// explicit release). The drill then proves per-domain fact survival with
// correct ownership, RLS fail-closed behavior, the restore-contract holds
// (marketing/AI/notifications), the 0130 grant-independence invariant, and
// that migrations converge to a clean no-op head on the restored database.
//
// The encrypted offsite leg (age, manifest v2, S3/SFTP transport) is already
// proven by restore_offsite_journey_test.go (OPS-002); this drill deliberately
// starts from the plaintext pg_dump artifact so the per-domain assertions stay
// readable and the runtime stays within the OPS-009 budget.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

const (
	ops009RestoreID   = "ops009-recovery-drill-0001"
	ops009TenantSlug  = "ops009-replay"
	ops009Domain      = "ops009.example.test"
	ops009BrandHost   = "panel.ops009.example.test"
	ops009DecoyBrand  = "ops009-decoy"
	ops009EvidenceHex = "ops009-evidence-body"
)

// ops009EvidenceDigest is the SHA-256 of the evidence body the seed stores
// through urmail_store_evidence_body (0098), which verifies the digest.
var ops009EvidenceDigest = func() string {
	sum := sha256.Sum256([]byte(ops009EvidenceHex))
	return hex.EncodeToString(sum[:])
}()

var (
	ops009PGBinResolved bool
	ops009PGBin         struct {
		dump, restore, psql string
	}
)

func ops009RequireBinaries(t *testing.T) {
	t.Helper()
	for _, name := range []string{"pg_dump", "pg_restore", "psql"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("PostgreSQL utilities unavailable; skipping OPS-009 drill")
		}
	}
	if !ops009PGBinResolved {
		dump, _ := exec.LookPath("pg_dump")
		restore, _ := exec.LookPath("pg_restore")
		psql, _ := exec.LookPath("psql")
		ops009PGBin.dump, ops009PGBin.restore, ops009PGBin.psql = dump, restore, psql
		ops009PGBinResolved = true
	}
}

func ops009RoleURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("role", "urmail_migrate")
	u.RawQuery = q.Encode()
	return u.String()
}

// ops009PSToolURL adapts a pgx-style DSN for the native PostgreSQL client
// tools: libpq rejects ?role=..., so the role moves into the options keyword.
func ops009PSToolURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	if role := q.Get("role"); role != "" {
		q.Del("role")
		options := "-c role=" + role
		if existing := q.Get("options"); existing != "" {
			options = existing + " " + options
		}
		q.Set("options", options)
	}
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	return u.String()
}

func ops009Migrate(t *testing.T, dsn string) {
	t.Helper()
	migrator, err := db.NewMigrator(ops009RoleURL(dsn))
	if err != nil {
		t.Fatal(err)
	}
	if err = migrator.Up(context.Background()); err != nil {
		_ = migrator.Close()
		t.Fatal(err)
	}
	if err = migrator.Close(); err != nil {
		t.Fatal(err)
	}
}

func ops009Connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func ops009SchemaVersion(t *testing.T, conn *pgx.Conn) (version int, dirty bool) {
	t.Helper()
	if err := conn.QueryRow(context.Background(), "SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	return version, dirty
}

// ops009Seed plants exactly one fact per domain that has never been through a
// restore drill before (0129 spam loop, 0084 collaboration, 0089-0092 archive
// investigation/approval/evidence/read, 0093 AI drafts, 0127 AI budgets,
// 0088/0100/0125 marketing flows, 0124 transport security, 0128 brand host
// lifecycle, notifications, 0130 compliance grants) plus the hold-relevant
// side-effect rows (0039 campaign marketing, 0044 AI usage, 0045 outbox) and
// the 0087 renewal fact. The archive approval is decided and its evidence
// manifest + body stored BEFORE the snapshot, so the restored row must carry
// the approved/evidence state, not re-derive it.
func ops009Seed(t *testing.T, dsn, evidenceDigest string) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	ctx := context.Background()
	if _, err := conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatal(err)
	}
	sourceDigest := strings.Repeat("a", 64)
	pendingDigest := strings.Repeat("b", 64)
	script := "DO $seed$ DECLARE\n" +
		" b bigint;t bigint;d bigint;m bigint;ab bigint;cal bigint;grp bigint;\n" +
		" ch bigint;tpl bigint;lst bigint;camp bigint;w bigint;flow bigint;\n" +
		" inv bigint;req1 bigint;req2 bigint;entry bigint;u1 bigint;u2 bigint;\n" +
		" BEGIN\n" +
		// Canonical ownership chain (brand/tenant/domain/mailbox) + 0126 settings + 0087 renewal.
		" INSERT INTO brands(slug,name) VALUES('ops009-source','OPS009 Source') RETURNING id INTO b;\n" +
		" INSERT INTO tenants(brand_id,name,slug,status) VALUES(b,'OPS009 Tenant','" + ops009TenantSlug + "','active') RETURNING id INTO t;\n" +
		" INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES(t,'" + ops009Domain + "','active','ops009-token',now()) RETURNING id INTO d;\n" +
		" INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES(t,d,'alice','alice@" + ops009Domain + "','ops009-hash','active') RETURNING id INTO m;\n" +
		" INSERT INTO mailbox_settings(mailbox_id,tenant_id,display_name,signature_text,preferences) VALUES(m,t,'Ops Nine','sent from the drill','{\"signature_default\":true}'::jsonb);\n" +
		" INSERT INTO subscription_renewal_attempts(tenant_id,subscription_id,period_end,state) VALUES(t,424242,now()+interval '24 hours','pending');\n" +
		// 0129 spam feedback loop: verdict, follow-up rule, learned tokens.
		" INSERT INTO mailbox_spam_feedback(tenant_id,mailbox_id,message_id,folder,verdict,sender_address,sender_domain) VALUES(t,m,'<ops009-msg-1@drill>','INBOX','spam','boss@spam.example.test','spam.example.test');\n" +
		" INSERT INTO mailbox_spam_senders(tenant_id,mailbox_id,scope,address,domain,list) VALUES(t,m,'mailbox','boss@spam.example.test','','deny');\n" +
		" INSERT INTO mailbox_spam_tokens(tenant_id,mailbox_id,token,spam_count,ham_count) VALUES(t,m,'winner',7,2);\n" +
		// 0084 collaboration (+0108 groups, 0111 attendees).
		" INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES(t,m,'ops009 contacts') RETURNING id INTO ab;\n" +
		" INSERT INTO mailbox_contacts(tenant_id,mailbox_id,addressbook_id,display_name,emails) VALUES(t,m,ab,'Ops Contact','[{\"value\":\"contact@ops009.example.test\"}]'::jsonb);\n" +
		" INSERT INTO mailbox_contact_groups(tenant_id,mailbox_id,addressbook_id,name) VALUES(t,m,ab,'ops009 group') RETURNING id INTO grp;\n" +
		" INSERT INTO mailbox_contact_group_members(tenant_id,mailbox_id,group_id,contact_id)\n" +
		"  SELECT t,m,grp,c.id FROM mailbox_contacts c WHERE c.tenant_id=t AND c.mailbox_id=m AND c.display_name='Ops Contact';\n" +
		" INSERT INTO mailbox_calendars(tenant_id,mailbox_id,name,timezone) VALUES(t,m,'ops009 calendar','Asia/Shanghai') RETURNING id INTO cal;\n" +
		" INSERT INTO mailbox_events(tenant_id,mailbox_id,calendar_id,uid,summary,starts_at,ends_at,timezone,attendees)\n" +
		"  VALUES(t,m,cal,'ops009-event-0001','drill standup',now()+interval '1 hour',now()+interval '2 hours','Asia/Shanghai','[{\"mailto\":\"contact@ops009.example.test\"}]'::jsonb);\n" +
		" INSERT INTO mailbox_sync_cursors(tenant_id,mailbox_id,protocol,client_key,cursor_id) VALUES(t,m,'web','ops009-client',1);\n" +
		// Grant actors: owner grants, auditor receives (0130 independence).
		" INSERT INTO users(email,password_hash) VALUES('ops009-owner@example.test','unused') RETURNING id INTO u1;\n" +
		" INSERT INTO users(email,password_hash) VALUES('ops009-auditor@example.test','unused') RETURNING id INTO u2;\n" +
		" INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES (u1,t,'owner'),(u2,t,'owner');\n" +
		// 0089 investigation + member + source over a real archive entry.
		" INSERT INTO archive_investigations(tenant_id,title,purpose,created_by,expires_at) VALUES(t,'ops009 case','drill investigation evidence',u1,now()+interval '7 days') RETURNING id INTO inv;\n" +
		" INSERT INTO archive_investigation_members(tenant_id,investigation_id,user_id) VALUES(t,inv,u2);\n" +
		" INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,received_at) VALUES(t,m,'<ops009-archive-1@drill>','cur/ops009-1',now()) RETURNING id INTO entry;\n" +
		" INSERT INTO archive_investigation_sources(tenant_id,investigation_id,archive_entry_id,added_by) VALUES(t,inv,entry,u1);\n" +
		// 0090 approvals: one decided pre-snapshot, one left pending for the
		// restored self-approval refusal probe.
		" INSERT INTO archive_approval_requests(tenant_id,investigation_id,requester_user_id,action,source_digest,expires_at)\n" +
		"  VALUES(t,inv,u1,'export','" + sourceDigest + "',now()+interval '1 day') RETURNING id INTO req1;\n" +
		" INSERT INTO archive_approval_requests(tenant_id,investigation_id,requester_user_id,action,source_digest,expires_at)\n" +
		"  VALUES(t,inv,u1,'export','" + pendingDigest + "',now()+interval '1 day') RETURNING id INTO req2;\n" +
		" IF urmail_decide_archive_approval(t,req1,u2,'approved','" + sourceDigest + "')<>'ok' THEN RAISE EXCEPTION 'seed: approval decide failed'; END IF;\n" +
		// 0091/0098 evidence manifest + body bound to the approved digest.
		" IF urmail_store_evidence_manifest(t,req1,u2,'" + sourceDigest + "','" + evidenceDigest + "')<>'ok' THEN RAISE EXCEPTION 'seed: evidence manifest failed'; END IF;\n" +
		" IF urmail_store_evidence_body(t,req1,u2,'" + evidenceDigest + "',convert_to('" + ops009EvidenceHex + "','UTF8'))<>'ok' THEN RAISE EXCEPTION 'seed: evidence body failed'; END IF;\n" +
		// 0130: independent compliance grant between two different users.
		" PERFORM set_config('urmail.tenant_id',t::text,true);\n" +
		" PERFORM set_config('urmail.user_id',u1::text,true);\n" +
		" INSERT INTO compliance_grants(tenant_id,user_id,scope,expires_at,reason) VALUES(t,u2,'export',now()+interval '7 days','ops009 drill independent grant');\n" +
		" PERFORM set_config('urmail.tenant_id','',true);\n" +
		" PERFORM set_config('urmail.user_id','',true);\n" +
		// 0093 AI draft + 0127 AI budget + 0044 reserved usage (held on restore).
		" INSERT INTO mailbox_ai_drafts(tenant_id,mailbox_id,subject,body,version) VALUES(t,m,'ops009 draft','drill draft body',1);\n" +
		" INSERT INTO ai_provider_configs(tenant_id,mode,provider,endpoint,model,daily_token_limit,monthly_cost_micros,enabled) VALUES(t,'platform','openai','https://ai.ops009.example.test','drill-model',123456,789,true);\n" +
		" INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,provider,model,cost_currency,input_rate,output_rate,reserved_tokens,reserved_cost_micros) VALUES(t,m,'ops009-ai-request-0001','" + sourceDigest + "',now()-interval '1 hour',now()+interval '1 hour','platform','openai','drill-model','CNY',1,1,100,75);\n" +
		// 0088/0100/0125 marketing flow with an uncertain delivery position.
		" INSERT INTO marketing_flows(tenant_id,name,status) VALUES(t,'ops009 welcome','active') RETURNING id INTO flow;\n" +
		" INSERT INTO marketing_flow_steps(tenant_id,flow_id,step_key,kind,next_key,position,wait_seconds) VALUES (t,flow,'s0','send','s1',0,0),(t,flow,'s1','wait','',1,3600);\n" +
		" INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key) VALUES(t,flow,'lead1@example.test','s0');\n" +
		" INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key,delivery_uncertain) VALUES(t,flow,'lead2@example.test','s1',true);\n" +
		// 0039 campaign marketing: hold semantics (queued/leased held, sent kept).
		" INSERT INTO marketing_channels(tenant_id,name,kind,sender_address,encrypted_config) VALUES(t,'ops009','dedicated_gateway','alice@ops009.example.test','enc:v1:fixture') RETURNING id INTO ch;\n" +
		" INSERT INTO marketing_templates(tenant_id,name,subject,text_body) VALUES(t,'ops009','subject','body') RETURNING id INTO tpl;\n" +
		" INSERT INTO marketing_lists(tenant_id,name) VALUES(t,'ops009') RETURNING id INTO lst;\n" +
		" INSERT INTO marketing_campaigns(tenant_id,name,channel_id,template_id,list_id,status) VALUES(t,'ops009',ch,tpl,lst,'running') RETURNING id INTO camp;\n" +
		" INSERT INTO marketing_quota_windows(tenant_id,starts_at,ends_at,timezone,used_count) VALUES(t,now()-interval '1 hour',now()+interval '23 hours','Asia/Shanghai',3) RETURNING id INTO w;\n" +
		" INSERT INTO marketing_recipients(tenant_id,campaign_id,email,status,lease_owner,lease_until,fence,window_id,reserved) VALUES\n" +
		"  (t,camp,'ops009-queued@example.test','queued',NULL,NULL,1,w,true),\n" +
		"  (t,camp,'ops009-leased@example.test','leased','old-source',now()+interval '1 hour',3,w,true),\n" +
		"  (t,camp,'ops009-sent@example.test','sent',NULL,NULL,5,w,true);\n" +
		// 0124 transport security + 0128 brand host lifecycle.
		" INSERT INTO domain_transport_security(tenant_id,domain_id,mtasts_policy_id,mtasts_mode,tls_rpt_rua) VALUES(t,d,'ops009-2026a','enforce','mailto:tlsrpt@ops009.example.test');\n" +
		" INSERT INTO brand_hosts(hostname,brand_id,status,verified_at) VALUES('" + ops009BrandHost + "',b,'verified',now());\n" +
		" INSERT INTO brand_host_challenges(hostname,token_digest,expires_at,consumed_at) VALUES('" + ops009BrandHost + "','" + pendingDigest + "',now()+interval '1 day',now());\n" +
		// 0045 notifications: an email lease to be held and an in-app row untouched.
		" INSERT INTO notification_outbox(tenant_id,kind,message_key,dedupe_key,channel,recipient,recipient_verified,lease_until) VALUES(t,'ops009','drill','ops009-email-1','email','verified@example.test',true,now()+interval '30 minutes');\n" +
		" INSERT INTO notification_outbox(tenant_id,kind,message_key,dedupe_key,channel) VALUES(t,'ops009','drill','ops009-inapp-1','in_app');\n" +
		"END $seed$;"
	if _, err := conn.Exec(ctx, script); err != nil {
		t.Fatal(err)
	}
}

func ops009RefusalErr(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55000"
}

// ops009BeginQuarantine activates the crash-window isolation marker on the
// restore target before any byte is restored.
func ops009BeginQuarantine(t *testing.T, dsn string) {
	t.Helper()
	pool, err := db.OpenPlatform(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.PlatformTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var active bool
	if err = tx.QueryRow(context.Background(), "SELECT urmail_begin_restore_quarantine()").Scan(&active); err != nil || !active {
		t.Fatalf("begin restore quarantine: active=%t %v", active, err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func ops009Release(t *testing.T, dsn string) bool {
	t.Helper()
	pool, err := db.OpenPlatform(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.PlatformTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var released bool
	if err = tx.QueryRow(context.Background(), "SELECT urmail_release_restore_quarantine()").Scan(&released); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return released
}

// ops009RestoreDatabaseArchive mirrors restoreDatabaseArchive with the
// PATH-resolved tools: pg_restore --list + db.FilterRestoreTOC, the filtered
// SQL produced with --no-owner, prefixed by db.RestoreSQLPrefix and consumed
// by psql as the NOSUPERUSER migration role in one transaction.
func ops009RestoreDatabaseArchive(ctx context.Context, dsn, dump, workDir string) error {
	list := exec.CommandContext(ctx, ops009PGBin.restore, "--list", "--", dump)
	var toc restoreTOCBuffer
	list.Stdout = &toc
	list.Stderr = io.Discard
	if err := list.Run(); err != nil {
		return fmt.Errorf("read restore object list: %w", err)
	}
	filtered, err := db.FilterRestoreTOC(toc.String())
	if err != nil {
		return err
	}
	listPath := filepath.Join(workDir, "ops009-restore.list")
	if err = os.WriteFile(listPath, []byte(filtered), 0o600); err != nil {
		return err
	}
	sqlPath := filepath.Join(workDir, "ops009-restore.sql")
	producer := exec.CommandContext(ctx, ops009PGBin.restore, "--no-owner", "--use-list", listPath, "--file=-", "--", dump)
	var sqlBuffer bytes.Buffer
	producer.Stdout = &sqlBuffer
	producer.Stderr = io.Discard
	if err = producer.Run(); err != nil {
		return fmt.Errorf("prepare complete restore SQL: %w", err)
	}
	full := db.RestoreSQLPrefix + sqlBuffer.String() + "\nCOMMIT;\n"
	if err = os.WriteFile(sqlPath, []byte(full), 0o600); err != nil {
		return err
	}
	consumer := exec.CommandContext(ctx, ops009PGBin.psql, "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--file", sqlPath, "--dbname", ops009PSToolURL(ops009RoleURL(dsn)))
	var stderr bytes.Buffer
	consumer.Stderr = &stderr
	if err = consumer.Run(); err != nil {
		return fmt.Errorf("atomic database restore failed; writers remain quarantined: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func ops009RestorePipeline(t *testing.T, dsn, dump, restoreID string) {
	t.Helper()
	ctx := context.Background()
	work := t.TempDir()
	if err := ops009RestoreDatabaseArchive(ctx, dsn, dump, work); err != nil {
		t.Fatal(err)
	}
	if err := db.FinalizeRestore(ctx, ops009RoleURL(dsn), restoreID); err != nil {
		t.Fatal(err)
	}
	migrator, err := db.NewMigrator(ops009RoleURL(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrator.Close() }()
	if err = migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.HoldRestoredSideEffects(ctx, ops009RoleURL(dsn), restoreID); err != nil {
		t.Fatal(err)
	}
	if err = db.VerifyRuntimePrivileges(ctx, ops009RoleURL(dsn)); err != nil {
		t.Fatal(err)
	}
}

// ops009AssertFacts proves every seeded fact survived with correct ownership.
func ops009AssertFacts(t *testing.T, dsn string, evidenceDigest, sourceDigest string) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	ctx := context.Background()
	if _, err := conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatal(err)
	}
	checks := []struct{ name, query string }{
		{"tenant/brand chain survived", `SELECT EXISTS(
		 SELECT 1 FROM tenants t JOIN brands b ON b.id=t.brand_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND b.slug='ops009-source' AND t.status='active')`},
		{"mailbox and domain survived", `SELECT EXISTS(
		 SELECT 1 FROM mailboxes m JOIN domains d ON d.id=m.domain_id AND d.tenant_id=m.tenant_id
		 JOIN tenants t ON t.id=m.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND d.fqdn='` + ops009Domain + `' AND m.address='alice@` + ops009Domain + `')`},
		{"target pre-restore fixture overwritten", `SELECT NOT EXISTS(SELECT 1 FROM brands WHERE slug='` + ops009DecoyBrand + `')`},
		{"0126 mailbox settings survived", `SELECT EXISTS(
		 SELECT 1 FROM mailbox_settings s JOIN mailboxes m ON m.id=s.mailbox_id AND m.tenant_id=s.tenant_id
		 WHERE m.address='alice@` + ops009Domain + `' AND s.display_name='Ops Nine'
		   AND s.signature_text='sent from the drill' AND s.preferences->>'signature_default'='true')`},
		{"0087 renewal attempt retained but held", `SELECT EXISTS(
		 SELECT 1 FROM subscription_renewal_attempts r JOIN tenants t ON t.id=r.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND r.subscription_id=424242 AND r.state='canceled' AND r.last_error LIKE '%RESTORE_RECONCILIATION_REQUIRED%')`},
		{"0129 spam feedback survived", `SELECT count(*)=1 AND bool_and(verdict='spam' AND sender_domain='spam.example.test' AND folder='INBOX')
		 FROM mailbox_spam_feedback f JOIN tenants t ON t.id=f.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND f.message_id='<ops009-msg-1@drill>'`},
		{"0129 spam sender rule survived", `SELECT EXISTS(
		 SELECT 1 FROM mailbox_spam_senders s JOIN tenants t ON t.id=s.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND s.scope='mailbox' AND s.address='boss@spam.example.test' AND s.list='deny')`},
		{"0129 spam token statistics survived", `SELECT EXISTS(
		 SELECT 1 FROM mailbox_spam_tokens k JOIN tenants t ON t.id=k.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND k.token='winner' AND k.spam_count=7 AND k.ham_count=2)`},
		{"0084 contact group with member survived", `SELECT EXISTS(
		 SELECT 1 FROM mailbox_contact_group_members gm
		 JOIN mailbox_contact_groups g ON g.tenant_id=gm.tenant_id AND g.mailbox_id=gm.mailbox_id AND g.id=gm.group_id
		 JOIN tenants t ON t.id=gm.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND g.name='ops009 group' AND g.deleted_at IS NULL)`},
		{"0084 contact survived in address book", `SELECT EXISTS(
		 SELECT 1 FROM mailbox_contacts c JOIN mailbox_addressbooks a ON a.tenant_id=c.tenant_id AND a.mailbox_id=c.mailbox_id AND a.id=c.addressbook_id
		 JOIN tenants t ON t.id=c.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND a.name='ops009 contacts' AND c.display_name='Ops Contact'
		   AND c.emails @> '[{"value":"contact@ops009.example.test"}]'::jsonb)`},
		{"0084 calendar event with attendee survived", `SELECT EXISTS(
		 SELECT 1 FROM mailbox_events e
		 JOIN mailboxes m ON m.tenant_id=e.tenant_id AND m.id=e.mailbox_id
		 JOIN tenants t ON t.id=m.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND e.uid='ops009-event-0001' AND e.summary='drill standup'
		   AND e.attendees @> '[{"mailto":"contact@ops009.example.test"}]'::jsonb AND e.status='confirmed')`},
		{"0084 sync cursor invalidated after rewind", `SELECT EXISTS(
		 SELECT 1 FROM mailbox_sync_cursors c JOIN tenants t ON t.id=c.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND c.protocol='web' AND c.client_key='ops009-client' AND c.cursor_id=0 AND c.epoch=urmail_restore_sync_epoch())`},
		{"0089 investigation survived open", `SELECT EXISTS(
		 SELECT 1 FROM archive_investigations i JOIN tenants t ON t.id=i.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND i.title='ops009 case' AND i.status='open')`},
		{"0089 membership survived", `SELECT EXISTS(
		 SELECT 1 FROM archive_investigation_members m
		 JOIN users u ON u.id=m.user_id JOIN tenants t ON t.id=m.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND u.email='ops009-auditor@example.test' AND m.removed_at IS NULL)`},
		{"0089 source bound to archive entry survived", `SELECT EXISTS(
		 SELECT 1 FROM archive_investigation_sources s
		 JOIN archive_entries e ON e.tenant_id=s.tenant_id AND e.id=s.archive_entry_id
		 JOIN tenants t ON t.id=s.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND e.message_id='<ops009-archive-1@drill>')`},
		{"0090 approval decision survived", `SELECT EXISTS(
		 SELECT 1 FROM archive_approval_requests r
		 JOIN users ap ON ap.id=r.approver_user_id JOIN tenants t ON t.id=r.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND r.source_digest='` + sourceDigest + `' AND r.state='approved'
		   AND ap.email='ops009-auditor@example.test' AND r.evidence_digest='` + evidenceDigest + `' AND r.decided_at IS NOT NULL)`},
		{"0090 pending request untouched by restore", `SELECT EXISTS(
		 SELECT 1 FROM archive_approval_requests r JOIN tenants t ON t.id=r.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND r.source_digest='` + strings.Repeat("b", 64) + `' AND r.state='pending' AND r.approver_user_id IS NULL)`},
		{"0093 AI draft survived", `SELECT EXISTS(
		 SELECT 1 FROM mailbox_ai_drafts dr JOIN tenants t ON t.id=dr.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND dr.subject='ops009 draft' AND dr.version=1)`},
		{"0127 AI budget limits survived", `SELECT EXISTS(
		 SELECT 1 FROM ai_provider_configs c JOIN tenants t ON t.id=c.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND c.daily_token_limit=123456 AND c.monthly_cost_micros=789 AND c.enabled)`},
		{"0088 marketing flow and steps survived", `SELECT EXISTS(
		 SELECT 1 FROM marketing_flows f JOIN tenants t ON t.id=f.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND f.name='ops009 welcome' AND f.status='paused'
		   AND (SELECT count(*) FROM marketing_flow_steps s WHERE s.tenant_id=f.tenant_id AND s.flow_id=f.id)=2
		   AND (SELECT bool_and(wait_seconds=3600) FROM marketing_flow_steps s WHERE s.tenant_id=f.tenant_id AND s.flow_id=f.id AND s.kind='wait'))`},
		{"0088 flow position retained subscribed and restore-fenced", `SELECT EXISTS(
		 SELECT 1 FROM marketing_flow_positions p JOIN tenants t ON t.id=p.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND p.email='lead1@example.test' AND p.step_key='s0'
		   AND NOT p.unsubscribed AND p.delivery_uncertain)`},
		{"0100 uncertain flow position survived", `SELECT EXISTS(
		 SELECT 1 FROM marketing_flow_positions p JOIN tenants t ON t.id=p.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND p.email='lead2@example.test' AND p.delivery_uncertain)`},
		{"0124 transport security policy survived", `SELECT EXISTS(
		 SELECT 1 FROM domain_transport_security s JOIN tenants t ON t.id=s.tenant_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND s.mtasts_policy_id='ops009-2026a' AND s.mtasts_mode='enforce'
		   AND s.tls_rpt_rua='mailto:tlsrpt@ops009.example.test')`},
		{"0128 brand hostname retained but restored ownership must be reverified", `SELECT EXISTS(
		 SELECT 1 FROM brand_hosts h JOIN brands b ON b.id=h.brand_id
		 WHERE h.hostname='` + ops009BrandHost + `' AND b.slug='ops009-source' AND h.status='pending-verify' AND h.verified_at IS NULL AND h.cert_issued_at IS NULL AND h.activated_at IS NULL AND h.failure_reason='RESTORE_RECONCILIATION_REQUIRED')`},
		{"0128 old DNS ownership challenge invalidated", `SELECT NOT EXISTS(
		 SELECT 1 FROM brand_host_challenges WHERE hostname='` + ops009BrandHost + `')`},
		{"0130 independent compliance grant survived", `SELECT EXISTS(
		 SELECT 1 FROM compliance_grants g
		 JOIN tenants t ON t.id=g.tenant_id
		 JOIN users gu ON gu.id=g.granted_by JOIN users ge ON ge.id=g.user_id
		 WHERE t.slug='` + ops009TenantSlug + `' AND gu.email='ops009-owner@example.test'
		   AND ge.email='ops009-auditor@example.test' AND g.scope='export' AND NOT g.revoked
		   AND g.owner_confirmed_at IS NOT NULL AND g.reason='ops009 drill independent grant')`},
		{"restored ownership is the runtime platform owner", fmt.Sprintf(`SELECT count(*)=%d AND bool_and(tableowner='urmail_platform') FROM pg_tables
		 WHERE schemaname='public' AND tablename IN (%s)`,
			len(ops009MaintenanceTables()), "'"+strings.Join(ops009MaintenanceTables(), "','")+"'")},
	}
	for _, check := range checks {
		var ok bool
		if err := conn.QueryRow(ctx, check.query).Scan(&ok); err != nil || !ok {
			t.Errorf("fact %s: %v (%v)", check.name, ok, err)
		}
	}
	ops009AssertEvidenceReadable(t, conn, evidenceDigest, sourceDigest)
}

// ops009AssertEvidenceReadable proves the 0091/0092/0098/0099 evidence chain:
// the stored body matches the approved digest and visibility is re-checked at
// read time for the member, and refused for the non-member requester.
func ops009AssertEvidenceReadable(t *testing.T, conn *pgx.Conn, evidenceDigest, sourceDigest string) {
	t.Helper()
	ctx := context.Background()
	var body []byte
	err := conn.QueryRow(ctx, `SELECT urmail_read_evidence_body(
	 (SELECT id FROM tenants WHERE slug='`+ops009TenantSlug+`'),
	 (SELECT id FROM archive_approval_requests WHERE source_digest='`+sourceDigest+`'),
	 (SELECT id FROM users WHERE email='ops009-auditor@example.test'),
	 '`+evidenceDigest+`')`).Scan(&body)
	if err != nil || string(body) != ops009EvidenceHex {
		t.Errorf("0092/0099 evidence body read back: %q %v", string(body), err)
	}
	var memberVisible, requesterVisible bool
	if err = conn.QueryRow(ctx, `SELECT urmail_evidence_manifest_visible(
	 (SELECT id FROM tenants WHERE slug='`+ops009TenantSlug+`'),
	 (SELECT id FROM archive_approval_requests WHERE source_digest='`+sourceDigest+`'),
	 (SELECT id FROM users WHERE email='ops009-auditor@example.test'),
	 '`+evidenceDigest+`')`).Scan(&memberVisible); err != nil || !memberVisible {
		t.Errorf("0092 member visibility lost: %v %v", memberVisible, err)
	}
	if err = conn.QueryRow(ctx, `SELECT urmail_evidence_manifest_visible(
	 (SELECT id FROM tenants WHERE slug='`+ops009TenantSlug+`'),
	 (SELECT id FROM archive_approval_requests WHERE source_digest='`+sourceDigest+`'),
	 (SELECT id FROM users WHERE email='ops009-owner@example.test'),
	 '`+evidenceDigest+`')`).Scan(&requesterVisible); err != nil || requesterVisible {
		t.Errorf("0092 visibility must not extend to the non-member requester: %v %v", requesterVisible, err)
	}
}

// ops009AssertHolds proves the restore contract holds on THIS fixture:
// marketing campaign work, AI reservation uncertainty and notification email
// delivery are held with provenance while completed facts stay untouched and
// the in-app notification is not held (email channel only per contract).
func ops009AssertHolds(t *testing.T, dsn string) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	ctx := context.Background()
	if _, err := conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatal(err)
	}
	checks := []struct{ name, query string }{
		{"marketing campaign paused", `SELECT status='paused' FROM marketing_campaigns`},
		{"queued marketing recipient held with quota and fence", `SELECT count(*)=1 AND bool_and(status='uncertain' AND reserved AND lease_owner IS NULL AND lease_until IS NULL AND fence=3)
		 FROM marketing_recipients WHERE email='ops009-queued@example.test'`},
		{"leased marketing recipient cleared and held", `SELECT count(*)=1 AND bool_and(status='uncertain' AND lease_owner IS NULL AND lease_until IS NULL AND fence=5)
		 FROM marketing_recipients WHERE email='ops009-leased@example.test'`},
		{"completed marketing fact untouched", `SELECT status='sent' AND fence=5 FROM marketing_recipients WHERE email='ops009-sent@example.test'`},
		{"AI reservation held with budget preserved", `SELECT status='uncertain' AND reserved_tokens=100 AND reserved_cost_micros=75
		 AND review_reason LIKE '%RESTORE_RECONCILIATION_REQUIRED%' FROM ai_usage_requests WHERE request_key='ops009-ai-request-0001'`},
		{"notification email held with lease invalidated", `SELECT state='uncertain' AND lease_until IS NULL AND fence=2
		 AND last_error LIKE '%RESTORE_RECONCILIATION_REQUIRED%' FROM notification_outbox WHERE dedupe_key='ops009-email-1'`},
		{"notification in-app not held", `SELECT state='pending' AND fence=0 FROM notification_outbox WHERE dedupe_key='ops009-inapp-1'`},
		{"marketing hold provenance recorded", `SELECT EXISTS(SELECT 1 FROM audit_logs WHERE action='restore.external_work_held'
		 AND detail->>'source_table'='marketing_recipients' AND detail->>'previous_state'='queued')`},
		{"AI hold provenance recorded", `SELECT EXISTS(SELECT 1 FROM audit_logs WHERE action='restore.external_work_held'
		 AND detail->>'source_table'='ai_usage_requests' AND detail->>'previous_state'='reserved')`},
		{"notification hold provenance recorded", `SELECT EXISTS(SELECT 1 FROM audit_logs WHERE action='restore.external_work_held'
		 AND detail->>'source_table'='notification_outbox' AND detail->>'previous_state'='pending')`},
		{"restore review recorded with this restore id", `SELECT EXISTS(SELECT 1 FROM settings WHERE scope='platform' AND key='restore_replay_review'
		 AND value->>'restore_id'='` + ops009RestoreID + `')`},
		{"restore reconciliation alert firing", `SELECT state='firing' FROM operational_alerts WHERE alert_key='restore:external_reconciliation'`},
	}
	for _, check := range checks {
		var ok bool
		if err := conn.QueryRow(ctx, check.query).Scan(&ok); err != nil || !ok {
			t.Errorf("hold %s: %v (%v)", check.name, ok, err)
		}
	}
}

// ops009AssertIndependence proves the 0130 invariant still fires on the
// restored database (self-grant refused) and that archive self-approval stays
// denied on the restored pending request.
func ops009AssertIndependence(t *testing.T, dsn, pendingDigest string) {
	t.Helper()
	ctx := context.Background()
	txConn := ops009Connect(t, dsn)
	if _, err := txConn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatal(err)
	}
	tx, err := txConn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('urmail.tenant_id',(SELECT id::text FROM tenants WHERE slug='`+ops009TenantSlug+`'),true),
	 set_config('urmail.user_id',(SELECT id::text FROM users WHERE email='ops009-owner@example.test'),true)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO compliance_grants(tenant_id,user_id,scope,expires_at,reason)
	 SELECT t.id,u.id,'export',now()+interval '7 days','ops009 self-grant probe'
	 FROM tenants t, users u WHERE t.slug='`+ops009TenantSlug+`' AND u.email='ops009-owner@example.test'`); err == nil {
		t.Fatal("0130 self-authorization must be refused on the restored database")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || !strings.Contains(pgErr.Message, "COMPLIANCE_GRANT_SELF_AUTHORIZATION") {
			t.Fatalf("0130 self-grant refused with the wrong error: %v", err)
		}
	}
	// The refused insert aborted the probe transaction; the archive
	// self-approval probe runs on its own connection.
	conn := ops009Connect(t, dsn)
	if _, err = conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatal(err)
	}
	var decision string
	if err = conn.QueryRow(ctx, `SELECT urmail_decide_archive_approval(
	 (SELECT id FROM tenants WHERE slug='`+ops009TenantSlug+`'),
	 (SELECT id FROM archive_approval_requests WHERE source_digest='`+pendingDigest+`'),
	 (SELECT id FROM users WHERE email='ops009-owner@example.test'),'approved','`+pendingDigest+`')`).Scan(&decision); err != nil || decision != "self_approval_denied" {
		t.Fatalf("restored archive self-approval must stay denied: %q %v", decision, err)
	}
}

// ops009AssertRLSFailClosed proves the restored RLS boundary: as urmail_app
// with no session context every per-domain table returns zero rows, a wrong
// tenant context still returns zero rows, and the correct tenant+mailbox
// context returns the seeded row.
func ops009AssertRLSFailClosed(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	conn := ops009Connect(t, dsn)
	// Resolve the fixture ids while still connected as the fixture owner;
	// after SET ROLE urmail_app the same lookup would be subject to RLS and
	// evaluate against the stale session context.
	var tenantID, mailboxID int64
	if err := conn.QueryRow(ctx, `SELECT t.id,m.id FROM tenants t
	 JOIN mailboxes m ON m.tenant_id=t.id
	 WHERE t.slug='`+ops009TenantSlug+`' AND m.address='alice@`+ops009Domain+`'`).Scan(&tenantID, &mailboxID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "SET ROLE urmail_app"); err != nil {
		t.Fatal(err)
	}
	noContext := []struct{ name, table string }{
		{"0129 spam feedback", "mailbox_spam_feedback"},
		{"0129 spam senders", "mailbox_spam_senders"},
		{"0129 spam tokens", "mailbox_spam_tokens"},
		{"0093 AI drafts", "mailbox_ai_drafts"},
		{"0088 marketing flows", "marketing_flows"},
		{"0089 investigations", "archive_investigations"},
		{"0087 renewal attempts", "subscription_renewal_attempts"},
		{"0045 notification outbox", "notification_outbox"},
		{"0130 compliance grants", "compliance_grants"},
	}
	for _, entry := range noContext {
		var count int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+entry.table).Scan(&count); err != nil || count != 0 {
			t.Errorf("RLS %s must fail closed without session context: count=%d %v", entry.name, count, err)
		}
	}
	if _, err := conn.Exec(ctx, "SELECT set_config('urmail.tenant_id','999999999',false)"); err != nil {
		t.Fatal(err)
	}
	var crossTenant int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM mailbox_spam_feedback").Scan(&crossTenant); err != nil || crossTenant != 0 {
		t.Errorf("RLS spam feedback must fail closed for a foreign tenant: count=%d %v", crossTenant, err)
	}
	if _, err := conn.Exec(ctx, `SELECT set_config('urmail.tenant_id',$1,false),
	 set_config('urmail.mailbox_id',$2,false)`, strconv.FormatInt(tenantID, 10), strconv.FormatInt(mailboxID, 10)); err != nil {
		t.Fatal(err)
	}
	var own int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM mailbox_spam_feedback").Scan(&own); err != nil || own != 1 {
		t.Errorf("RLS own-context spam feedback must return the restored row: count=%d %v", own, err)
	}
	var tokens int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM mailbox_spam_tokens").Scan(&tokens); err != nil || tokens != 1 {
		t.Errorf("RLS own-context spam tokens must return the restored row: count=%d %v", tokens, err)
	}
}

// ops009AssertConvergedMigrations proves the upgrade side: the restored
// schema version equals the source head, is not dirty, and further Up calls
// are true no-ops.
func ops009AssertConvergedMigrations(t *testing.T, dsn string, sourceVersion int) {
	t.Helper()
	ctx := context.Background()
	conn := ops009Connect(t, dsn)
	version, dirty := ops009SchemaVersion(t, conn)
	if version != sourceVersion || dirty {
		t.Fatalf("restored schema did not converge: version=%d dirty=%t (source %d)", version, dirty, sourceVersion)
	}
	for i := 0; i < 2; i++ {
		migrator, err := db.NewMigrator(ops009RoleURL(dsn))
		if err != nil {
			t.Fatal(err)
		}
		err = migrator.Up(ctx)
		_ = migrator.Close()
		if err != nil {
			t.Fatalf("post-restore migrator.Up #%d must be a no-op: %v", i+1, err)
		}
	}
	after, dirtyAfter := ops009SchemaVersion(t, conn)
	if after != sourceVersion || dirtyAfter {
		t.Fatalf("no-op migrations disturbed the restored version: version=%d dirty=%t", after, dirtyAfter)
	}
}

// ops009MaintenanceTables lists the per-domain tables whose ownership must be
// the platform runtime role after FinalizeRestore.
func ops009MaintenanceTables() []string {
	return []string{
		"tenants", "domains", "mailboxes", "mailbox_settings",
		"mailbox_spam_feedback", "mailbox_spam_senders", "mailbox_spam_tokens",
		"mailbox_addressbooks", "mailbox_contacts", "mailbox_contact_groups", "mailbox_contact_group_members",
		"mailbox_calendars", "mailbox_events", "mailbox_changes", "mailbox_sync_cursors",
		"archive_investigations", "archive_investigation_members", "archive_investigation_sources", "archive_approval_requests",
		"mailbox_ai_drafts", "ai_provider_configs", "ai_usage_requests",
		"marketing_flows", "marketing_flow_steps", "marketing_flow_positions",
		"marketing_channels", "marketing_campaigns", "marketing_recipients", "marketing_quota_windows",
		"recurring_setup_intents", "mailbox_import_jobs", "mailbox_import_items", "recall_requests", "domain_dns_security", "domain_tls_reports", "domain_transport_security", "brand_hosts", "brand_host_challenges",
		"notification_outbox", "subscription_renewal_attempts", "compliance_grants",
	}
}

// TestOPS009RecoveryDrill is the OPS-009 proof: a real pg_dump of a database
// seeded with one fact per domain restores into a second fresh database with
// facts, ownership, RLS, holds, the 0130 independence trigger and migration
// convergence all intact, gated by quarantine isolation and explicit release.
func TestOPS009RecoveryDrill(t *testing.T) {
	ops009RequireBinaries(t)
	ctx, cancel := context.WithTimeout(context.Background(), 420*time.Second)
	defer cancel()

	source := testdb.New(t)
	ops009Migrate(t, source)
	sourceConn := ops009Connect(t, source)
	sourceVersion, dirty := ops009SchemaVersion(t, sourceConn)
	if dirty {
		t.Fatalf("source schema is dirty at version %d", sourceVersion)
	}
	ops009Seed(t, source, ops009EvidenceDigest)
	ops009SeedCalendarRecurring(t, source)
	ops009SeedRecurringSetups(t, source)
	ops009SeedRecallTransport(t, source)
	ops009SeedMailImports(t, source)
	ops009SeedAIQuota(t, source)

	// 1. Real pg_dump of the fully seeded database (custom format).
	spool := t.TempDir()
	dumpPath := filepath.Join(spool, "ops009.dump")
	dump := exec.CommandContext(ctx, ops009PGBin.dump, "--format=custom", "--file", dumpPath, "--dbname", source)
	if err := dump.Run(); err != nil {
		t.Fatalf("pg_dump: %v", err)
	}

	// 2. SECOND fresh database as the restore target, migrated to the same
	// head, with a pre-restore decoy that the overwrite must remove.
	target := testdb.New(t)
	ops009Migrate(t, target)
	targetVersion, targetDirty := ops009SchemaVersion(t, ops009Connect(t, target))
	if targetVersion != sourceVersion || targetDirty {
		t.Fatalf("target fixture did not reach the source head: %d/%t", targetVersion, targetDirty)
	}
	targetConn := ops009Connect(t, target)
	if _, err := targetConn.Exec(ctx, "INSERT INTO brands(slug,name) VALUES('"+ops009DecoyBrand+"','Old target fixture')"); err != nil {
		t.Fatal(err)
	}

	// 3. Crash-window isolation marker before any restored byte lands.
	ops009BeginQuarantine(t, target)
	if _, err := targetConn.Exec(ctx, "INSERT INTO tenants(brand_id,name,slug,status) SELECT id,'Fence Probe','ops009-fence-probe','active' FROM brands WHERE slug='"+ops009DecoyBrand+"'"); !ops009RefusalErr(err) {
		t.Fatalf("new tenant must be refused while restore quarantine is active, got %v", err)
	}

	// 4. Real restore pipeline: TOC filter -> atomic SQL restore as the
	// migration role -> ownership finalization -> migrations -> side-effect
	// holds -> runtime privilege verification.
	ops009RestorePipeline(t, target, dumpPath, ops009RestoreID)
	if err := db.VerifyRestoreIdentity(ctx, ops009RoleURL(target), ops009RestoreID); err != nil {
		t.Fatal(err)
	}
	if err := db.VerifyRestoreIdentity(ctx, ops009RoleURL(target), "another-restore"); err == nil {
		t.Fatal("restore identity must not match another restore id")
	}

	// 5. Per-domain evidence on the restored database.
	ops009AssertFacts(t, target, ops009EvidenceDigest, strings.Repeat("a", 64))
	ops009AssertHolds(t, target)
	ops009AssertIndependence(t, target, strings.Repeat("b", 64))
	ops009AssertRLSFailClosed(t, target)
	ops009AssertAIQuota(t, target, true)
	ops009AssertConvergedMigrations(t, target, sourceVersion)
	ops009AssertAIQuota(t, target, true)

	// 6. Explicit release only after every step succeeded.
	if !ops009Release(t, target) {
		t.Fatal("explicit release refused")
	}
	ops009AssertCalendarRecurring(t, target)
	ops009AssertRecurringSetups(t, target)
	ops009AssertRecallTransport(t, target)
	ops009AssertMailImports(t, target)
	if _, err := targetConn.Exec(ctx, "INSERT INTO brands(slug,name) VALUES('ops009-post-release','After release')"); err != nil {
		t.Fatalf("release must restore normal writability: %v", err)
	}
}
