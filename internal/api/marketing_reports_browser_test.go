package api_test

import (
	"crypto/x509"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/marketing"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
)

// This opt-in fixture uses production login/router/CSRF and real PostgreSQL.
// Its two accepted messages travel through the production workers to a local
// verified-TLS SMTP receiver; the queued cohort is explicitly synthetic.
func TestMarketingReportsBrowserFixture(t *testing.T) {
	dir := os.Getenv("URMAIL_MARKETING_REPORT_BROWSER_DIR")
	if dir == "" {
		t.Skip("private browser fixture directory required")
	}
	app, platform := remediationHTTPDatabase(t)
	ctx := t.Context()
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if e := platform.QueryRow(ctx, sql, args...).Scan(&id); e != nil {
			t.Fatal(e)
		}
		return id
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := platform.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('default','Report Browser') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Outcome Browser','outcome-browser','active') RETURNING id", brand)
	password := "isolated-outcome-browser-Strong-password"
	hash, e := domainMail.HashPassword(password)
	if e != nil {
		t.Fatal(e)
	}
	actor := scalar("INSERT INTO users(email,password_hash) VALUES('outcomes-owner@fixture.test',$1) RETURNING id", hash)
	exec("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", actor, tenant)
	exec(`INSERT INTO plans(code,name,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features) VALUES('outcomes-browser','{"en":"Isolated paid fixture","zh-CN":"隔离付费测试"}',100,'CNY',1073741824,5,2,1000,'{"marketing_daily_recipient_limit":1000,"marketing_rate_per_minute":60000}')`)
	exec("UPDATE subscriptions SET status='canceled' WHERE tenant_id=$1", tenant)
	exec("INSERT INTO subscriptions(tenant_id,plan_id,status,period_start,period_end,plan_snapshot) SELECT $1,code,'active',now(),now()+interval '1 month',to_jsonb(p) FROM plans p WHERE code='outcomes-browser'", tenant)
	tx, e := platform.PlatformTx(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var start time.Time
	if e = tx.QueryRow(ctx, "SELECT period_start FROM subscriptions WHERE tenant_id=$1", tenant).Scan(&start); e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(ctx, "INSERT INTO billing_calendars(tenant_id,timezone,first_paid_at,anchor_at,source,review_reason,created_by) VALUES($1,'Asia/Shanghai',$2,$2,'manual_legacy','Isolated browser fixture known paid instant and timezone',$3) ON CONFLICT (tenant_id) DO NOTHING", tenant, start, actor)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'marketing-browser.example.test','active',now(),'fixture') RETURNING id", tenant)
	exec("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,send_enabled) VALUES($1,$2,'sender','sender@marketing-browser.example.test','unused',268435456,'active',true)", tenant, domain)
	box, e := secrets.New([]byte(strings.Repeat("b", 32)))
	if e != nil {
		t.Fatal(e)
	}
	tokens, e := domainMail.NewSuppressionManager([]byte(strings.Repeat("u", 32)))
	if e != nil {
		t.Fatal(e)
	}
	cfg, roots, capture := browserMarketingGateway(t)
	t.Setenv("GODEBUG", "x509usefallbackroots=1")
	x509.SetFallbackRoots(roots)
	cfgRaw, _ := json.Marshal(cfg)
	encrypted, e := box.EncryptString(string(cfgRaw))
	if e != nil {
		t.Fatal(e)
	}
	channel := scalar("INSERT INTO marketing_channels(tenant_id,name,kind,sender_address,encrypted_config,enabled) VALUES($1,'Verified TLS local','dedicated_gateway',$2,$3,true) RETURNING id", tenant, cfg.From, encrypted)
	template := scalar("INSERT INTO marketing_templates(tenant_id,name,subject,text_body) VALUES($1,'Outcome synthetic','Outcome synthetic','Only isolated authored content') RETURNING id", tenant)
	list := scalar("INSERT INTO marketing_lists(tenant_id,name) VALUES($1,'Outcome cohort') RETURNING id", tenant)
	exec("INSERT INTO marketing_contacts(tenant_id,list_id,email,opted_in,consent_source) VALUES($1,$2,'accepted@fixture.test',true,'isolated authored fixture'),($1,$2,'flow-accepted@fixture.test',true,'isolated authored fixture')", tenant, list)
	campaign := scalar("INSERT INTO marketing_campaigns(tenant_id,name,channel_id,template_id,list_id,status,scheduled_at) VALUES($1,'Outcome campaign',$2,$3,$4,'scheduled',now()) RETURNING id", tenant, channel, template, list)
	exec("INSERT INTO marketing_recipients(tenant_id,campaign_id,email,status) VALUES($1,$2,'accepted@fixture.test','queued')", tenant, campaign)
	worker, e := marketing.NewWorker(marketing.Options{Pool: platform, Secrets: box, Tokens: tokens, Enabled: true, Hostname: "box.example.test", UnsubscribeURL: "https://box.example.test/api/v1/marketing/unsubscribe"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = worker.Step(ctx); e != nil {
		t.Fatal(e)
	}
	if scalar("SELECT count(*) FROM marketing_recipients WHERE tenant_id=$1 AND campaign_id=$2 AND status='sent'", tenant, campaign) != 1 {
		t.Fatal("SMTP acceptance was not persisted")
	}
	exec("UPDATE marketing_campaigns SET status='paused' WHERE tenant_id=$1 AND id=$2", tenant, campaign)
	exec("INSERT INTO marketing_recipients(tenant_id,campaign_id,email,status) SELECT $1,$2,'queued-'||i||'@fixture.test','queued' FROM generate_series(1,54)i", tenant, campaign)
	definition := scalar("INSERT INTO marketing_flow_definitions(tenant_id,name,draft,status,published_version) VALUES($1,'Outcome flow','{}','active',1) RETURNING id", tenant)
	runtime := scalar("INSERT INTO marketing_flows(tenant_id,name,status) VALUES($1,'Outcome flow runtime','active') RETURNING id", tenant)
	exec("INSERT INTO marketing_flow_steps(tenant_id,flow_id,step_key,kind,position,next_key) VALUES($1,$2,'send','send',0,'stop'),($1,$2,'stop','stop',1,'')", tenant, runtime)
	version := scalar("INSERT INTO marketing_flow_versions(tenant_id,definition_id,version,draft_revision,runtime_flow_id,definition,published_by) VALUES($1,$2,1,1,$3,'{}',$4) RETURNING id", tenant, definition, runtime, actor)
	exec("INSERT INTO marketing_flow_content_snapshots(tenant_id,runtime_flow_id,step_key,template_id,subject,text_body) VALUES($1,$2,'send',$3,'Flow synthetic','Only isolated authored content')", tenant, runtime, template)
	entry := scalar("INSERT INTO marketing_flow_entries(tenant_id,definition_id,version_id,event_key,source,list_id,recipient_count) VALUES($1,$2,$3,'browser-outcome-entry','contact_enrolled',$4,1) RETURNING id", tenant, definition, version, list)
	exec("INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key,entry_id) VALUES($1,$2,'flow-accepted@fixture.test','send',$3)", tenant, runtime, entry)
	sender := &marketing.FlowSender{Pool: platform, Secrets: box, Tokens: tokens, Hostname: "box.example.test", UnsubscribeURL: "https://box.example.test/api/v1/marketing/unsubscribe", Content: marketing.TemplateFlowContent(platform)}
	fw, e := marketing.NewFlowWorker(marketing.FlowOptions{Pool: platform, Enabled: true, Sender: sender.Send})
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(5 * time.Millisecond)
	if _, e = fw.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if scalar("SELECT count(*) FROM marketing_flow_node_executions WHERE tenant_id=$1 AND status='submitted'", tenant) != 1 {
		t.Fatal("real flow SMTP accepted fact missing")
	}
	messages, e := i18n.NewStore()
	if e != nil {
		t.Fatal(e)
	}
	q := store.New(platform.Pool)
	server := httptest.NewServer(api.New(api.Options{Logger: zap.NewNop(), Messages: messages, AppPool: app, PlatformPool: platform, Audit: q, Secrets: box, MarketingTokens: tokens, MarketingEnabled: true, QuoteHMACKey: strings.Repeat("q", 32)}))
	defer server.Close()
	if e = os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(map[string]any{"url": server.URL, "email": "outcomes-owner@fixture.test", "password": password, "tenant_id": tenant, "campaign_id": campaign, "flow_id": definition, "capture_url": capture})
	if e = os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	t.Log("READY: production auth/router + real PG + campaign/flow verified TLS SMTP accepted; waiting for browser")
	deadline := time.After(20 * time.Minute)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("browser timed out")
		case <-ticker.C:
			if done, e := os.ReadFile(filepath.Join(dir, "browser.done")); e == nil {
				if strings.TrimSpace(string(done)) != "PASS" {
					t.Fatal("browser failed")
				}
				return
			}
		}
	}
}
