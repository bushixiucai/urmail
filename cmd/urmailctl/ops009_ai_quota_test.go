package main

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

// These facts cross the actual custom-format archive and isolated restore, not
// only the quarantine helper. The finite-plan row intentionally carries a
// different period_start: independent quotas must account by creation window.
func ops009SeedAIQuota(t *testing.T, dsn string) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	if _, err := conn.Exec(t.Context(), `SET ROLE urmail_platform`); err != nil {
		t.Fatal(err)
	}
	_, err := conn.Exec(t.Context(), `DO $$ DECLARE tid bigint;mid bigint; BEGIN
 SELECT id INTO tid FROM tenants WHERE slug='ops009-replay';
 SELECT id INTO mid FROM mailboxes WHERE tenant_id=tid LIMIT 1;
 UPDATE ai_provider_configs SET mode='byo',nonplatform_call_limit=10,nonplatform_mailbox_call_limit=5,nonplatform_concurrency_limit=1 WHERE tenant_id=tid;
 INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,provider,model,cost_currency,input_rate,output_rate,status,reserved_tokens,reserved_cost_micros,actual_tokens,actual_cost_micros)
 VALUES
 (tid,mid,'ops009-quota-old-finite',repeat('d',64),now()-interval '17 days',now()+interval '13 days','byo','openai','old-finite','CNY',0,0,'completed',100,0,100,0),
 (tid,mid,'ops009-quota-local-unknown',repeat('e',64),now()-interval '3 days',now()+interval '27 days','local','ollama','configured','CNY',0,0,'uncertain',200,0,NULL,NULL),
 (tid,mid,'ops009-quota-byo-inflight',repeat('f',64),now()-interval '2 days',now()+interval '28 days','byo','openai','configured','CNY',0,0,'reserved',300,0,NULL,NULL);
 END $$`)
	if err != nil {
		t.Fatal(err)
	}
	ops009AssertAIQuota(t, dsn, false)
}

func ops009AssertAIQuota(t *testing.T, dsn string, restored bool) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	if _, err := conn.Exec(t.Context(), `SET ROLE urmail_platform`); err != nil {
		t.Fatal(err)
	}
	var tid, mid int64
	if err := conn.QueryRow(t.Context(), `SELECT t.id,m.id FROM tenants t JOIN mailboxes m ON m.tenant_id=t.id WHERE t.slug='ops009-replay' LIMIT 1`).Scan(&tid, &mid); err != nil {
		t.Fatal(err)
	}
	q := store.New(conn)
	cfg, err := q.PlatformGetAIProvider(t.Context(), tid)
	if err != nil || cfg.NonplatformCallLimit != 10 || cfg.NonplatformMailboxCallLimit != 5 || cfg.NonplatformConcurrencyLimit != 1 || cfg.QuotaTimezone != "Asia/Shanghai" || cfg.Mode != "byo" {
		t.Fatalf("restored independent quota configuration: %+v %v", cfg, err)
	}
	zone, err := time.LoadLocation(cfg.QuotaTimezone)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(zone)
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, zone)
	usage, err := q.PlatformAIQuotaUsage(t.Context(), store.PlatformAIQuotaUsageParams{TenantID: tid, MailboxID: mid, IndependentWindow: true, PeriodStart: pgtype.Timestamptz{Time: start, Valid: true}, PeriodEnd: pgtype.Timestamptz{Time: start.AddDate(0, 1, 0), Valid: true}})
	slots := int64(1)
	if restored {
		slots = 0
	}
	if err != nil || usage.CallsUsed != 3 || usage.MailboxCallsUsed != 3 || usage.ConcurrentReserved != slots {
		t.Fatalf("restored independent calls/mailbox/slots: %+v %v", usage, err)
	}
	var tokens int64
	var status string
	if err = conn.QueryRow(t.Context(), `SELECT sum(COALESCE(actual_tokens,reserved_tokens)) FROM ai_usage_requests WHERE tenant_id=$1 AND request_key LIKE 'ops009-quota-%'`, tid).Scan(&tokens); err != nil || tokens != 600 {
		t.Fatalf("restored token reservations lost: %d %v", tokens, err)
	}
	if err = conn.QueryRow(t.Context(), `SELECT status FROM ai_usage_requests WHERE tenant_id=$1 AND request_key='ops009-quota-byo-inflight'`, tid).Scan(&status); err != nil {
		t.Fatal(err)
	}
	expected := "reserved"
	if restored {
		expected = "uncertain"
	}
	if status != expected {
		t.Fatalf("restore must preserve unknown call/token debit while retiring dead concurrency slot: %s", status)
	}
}
