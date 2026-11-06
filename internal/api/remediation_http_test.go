package api_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/marketing"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func remediationHTTPDatabase(t *testing.T) (*db.Pool, *db.Pool) {
	t.Helper()
	dsn := testdb.New(t)
	ctx := context.Background()
	migrator, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	err = migrator.Up(ctx)
	_ = migrator.Close()
	if err != nil {
		t.Fatal(err)
	}
	app, err := db.OpenApp(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	platform, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(platform.Close)
	return app, platform
}

// This uses the complete production router and real HTTP connections, plus a
// local HTTP Stripe stand-in. It never contacts a payment provider or DNS API.
func TestRemediationBrowserWithRealRoles(t *testing.T) {
	if os.Getenv("URMAIL_BROWSER_FIXTURE_DIR") == "" {
		t.Skip("live browser harness requires an explicit private fixture directory")
	}
	app, platform := remediationHTTPDatabase(t)
	ctx := t.Context()
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := platform.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := platform.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('default','Browser Default') RETURNING id")
	staff := scalar("INSERT INTO users(email,password_hash) VALUES('http-a@example.test','placeholder') RETURNING id")
	ownerPassword := "browser-owner-Strong-fixture-password"
	hash, err := domainMail.HashPassword(ownerPassword)
	if err != nil {
		t.Fatal(err)
	}
	owner := scalar("INSERT INTO users(email,password_hash) VALUES('browser-owner@example.test',$1) RETURNING id", hash)
	staffTenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Browser Platform','browser-platform','active') RETURNING id", brand)
	ownerTenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Browser Tenant','browser-tenant','pending') RETURNING id", brand)
	exec("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner'),($3,$4,'owner')", staff, staffTenant, owner, ownerTenant)
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", staff)
	q := store.New(platform.Pool)
	verifiedStaff, verifyErr := q.GetUserByID(ctx, staff)
	if verifyErr != nil {
		t.Fatal(verifyErr)
	}
	if _, err = q.CreateSession(ctx, store.CreateSessionParams{ExpectedHash: verifiedStaff.PasswordHash, ExpectedVersion: verifiedStaff.CredentialVersion, ID: middleware.HashSessionToken("http-token-a"), UserID: staff, TenantID: pgtype.Int8{Int64: staffTenant, Valid: true}, CsrfToken: "test-csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New([]byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := domainMail.NewSuppressionManager([]byte(strings.Repeat("u", 32)))
	if err != nil {
		t.Fatal(err)
	}
	options := api.Options{Logger: zap.NewNop(), Messages: messages, AppPool: app, PlatformPool: platform, Version: "browser-test", Audit: q, BaseURL: "https://panel.example.test", QuoteHMACKey: "test-quote-signing-key-at-least-32-bytes", Secrets: box, MarketingTokens: tokens}
	options.AICloudEndpoints = []string{"https://models.example.test/v1"}
	service, dnsFixture := browserDNSService(t, platform, box)
	options.DNSPlans = service
	server := httptest.NewServer(api.New(options))
	defer server.Close()
	raw, err := json.Marshal(map[string]any{"base_url": server.URL, "accounts": []map[string]any{{"email": "browser-owner@example.test", "password": ownerPassword, "tenant_id": ownerTenant}}})
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(t.TempDir(), "browser-seed.json")
	if err = os.WriteFile(fixturePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	holdRemediationBrowser(t, platform, options, fixturePath, staff, dnsFixture)
}

func holdRemediationBrowser(t *testing.T, platform *db.Pool, options api.Options, fixturePath string, userA int64, dnsBrowserFixture map[string]any) {
	t.Helper()
	evidenceDir := os.Getenv("URMAIL_BROWSER_FIXTURE_DIR")
	if evidenceDir == "" {
		return
	}
	ctx := t.Context()
	q := store.New(platform.Pool)
	box := options.Secrets
	tokens, ok := options.MarketingTokens.(*domainMail.SuppressionManager)
	if !ok {
		t.Fatal("browser tokens not configured")
	}
	var err error
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var n int64
		if queryErr := platform.QueryRow(ctx, sql, args...).Scan(&n); queryErr != nil {
			t.Fatal(queryErr)
		}
		return n
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, execErr := platform.Exec(ctx, sql, args...); execErr != nil {
			t.Fatal(execErr)
		}
	}
	fixture, readErr := os.ReadFile(fixturePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	quarantineOptions := options
	quarantineOptions.Quarantined = true
	quarantineServer := httptest.NewServer(api.New(quarantineOptions))
	defer quarantineServer.Close()
	var browserFixture map[string]any
	if err = json.Unmarshal(fixture, &browserFixture); err != nil {
		t.Fatal(err)
	}
	browserAccounts := browserFixture["accounts"].([]any)
	firstAccount := browserAccounts[0].(map[string]any)
	browserTenant := int64(firstAccount["tenant_id"].(float64))
	platformPassword := "browser-platform-Strong-fixture-password"
	platformHash, hashErr := domainMail.HashPassword(platformPassword)
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	exec("UPDATE users SET password_hash=$1 WHERE id=$2", platformHash, userA)
	browserFixture["platform_account"] = map[string]any{"email": "http-a@example.test", "password": platformPassword}
	cfg, roots, captureURL := browserMarketingGateway(t)
	// The test-only process trusts this ephemeral local CA. Production SendSMTP
	// is used unchanged, with verified TLS and no fake sender or TLS bypass.
	t.Setenv("GODEBUG", "x509usefallbackroots=1")
	x509.SetFallbackRoots(roots)
	exec("UPDATE tenants SET status='active' WHERE id=$1", browserTenant)
	browserDomain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'marketing-browser.example.test','active',now(),'local-fixture-proof') RETURNING id", browserTenant)
	exec("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,send_enabled) VALUES($1,$2,'sender','sender@marketing-browser.example.test','unused',268435456,'active',true)", browserTenant, browserDomain)
	cfgRaw, _ := json.Marshal(cfg)
	encrypted, cipherErr := box.EncryptString(string(cfgRaw))
	if cipherErr != nil {
		t.Fatal(cipherErr)
	}
	channel, channelErr := q.PlatformCreateMarketingChannel(ctx, store.PlatformCreateMarketingChannelParams{TenantID: browserTenant, Name: "Browser local verified TLS", Kind: "dedicated_gateway", SenderAddress: cfg.From, EncryptedConfig: encrypted, Enabled: true})
	if channelErr != nil {
		t.Fatal(channelErr)
	}
	unsubscribeServer := httptest.NewTLSServer(api.New(options))
	defer unsubscribeServer.Close()
	workerContext, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	worker, workerErr := marketing.NewWorker(marketing.Options{Pool: platform, Secrets: box, Tokens: tokens, Enabled: true, Hostname: "box.example.test", UnsubscribeURL: unsubscribeServer.URL + "/api/v1/marketing/unsubscribe"})
	if workerErr != nil {
		t.Fatal(workerErr)
	}
	go worker.Run(workerContext)
	browserMailbox := scalar("SELECT id FROM mailboxes WHERE tenant_id=$1 AND address='sender@marketing-browser.example.test'", browserTenant)
	aiUsageID := scalar("INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,provider,model,cost_currency,input_rate,output_rate,status,reserved_tokens,reserved_cost_micros,updated_at) VALUES($1,$2,'browser-uncertain-usage',repeat('a',64),now(),now()+interval '1 month','platform','openai','browser-model','USD',1000000,2000000,'uncertain',100,300,now()-interval '10 minutes') RETURNING id", browserTenant, browserMailbox)
	browserFixture["ai_api_key"] = "browser-local-only-api-fixture-key"
	browserFixture["ai_usage_id"] = aiUsageID
	dnsBrowserFixture["domain_id"] = browserDNSSeed(t, platform, browserTenant)
	browserFixture["dns"] = dnsBrowserFixture
	browserFixture["marketing"] = map[string]any{"channel_id": channel.ID, "mail_capture_url": captureURL, "feedback_key": cfg.FeedbackKey}
	browserFixture["quarantine_url"] = quarantineServer.URL
	fixture, err = json.Marshal(browserFixture)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(evidenceDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(evidenceDir, "browser-fixture.json"), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("Browser harness ready; waiting for browser.done (maximum five minutes)")
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
waitBrowser:
	for {
		select {
		case <-deadline.C:
			t.Fatal("browser acceptance did not finish within five minutes")
		case <-ticker.C:
			if outcome, readErr := os.ReadFile(filepath.Join(evidenceDir, "browser.done")); readErr == nil {
				if strings.TrimSpace(string(outcome)) != "PASS" {
					t.Fatal("browser runner reported failure")
				}
				break waitBrowser
			}
		}
	}
	facts := map[string]int64{
		"ai_explicit_zero_release":         scalar("SELECT count(*) FROM ai_usage_requests WHERE tenant_id=$1 AND status='released' AND actual_tokens=0 AND actual_cost_micros=0 AND reviewed_by=$2", browserTenant, userA),
		"support_staff_replies":            scalar("SELECT count(*) FROM ticket_messages WHERE tenant_id=$1 AND is_staff", browserTenant),
		"support_reply_notices":            scalar("SELECT count(*) FROM notification_outbox WHERE tenant_id=$1 AND kind='support_reply' AND state='delivered'", browserTenant),
		"marketing_feedback_events":        scalar("SELECT count(*) FROM marketing_feedback WHERE tenant_id=$1", browserTenant),
		"marketing_complaints":             scalar("SELECT count(*) FROM marketing_recipients WHERE tenant_id=$1 AND status='bounced' AND result_code='complaint'", browserTenant),
		"marketing_permanent_suppressions": scalar("SELECT count(*) FROM marketing_suppressions WHERE tenant_id=$1 AND reason='unsubscribe'", browserTenant),
		"marketing_original_window_usage":  scalar("SELECT COALESCE(sum(used_count),0) FROM marketing_quota_windows WHERE tenant_id=$1", browserTenant),
	}
	for name, count := range facts {
		if count != 1 {
			t.Fatalf("browser closure fact %s=%d, want exactly1", name, count)
		}
	}
	closureFacts, _ := json.MarshalIndent(facts, "", "  ")
	if err = os.WriteFile(filepath.Join(evidenceDir, "closure-facts.json"), closureFacts, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("PASS: real browser support reply and notification; one verified local TLS SMTP delivery, inert GET/explicit POST unsubscribe, one deduplicated signed complaint and original quota window")
}
