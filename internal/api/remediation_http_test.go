package api_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/bushixiucai/urmail/internal/payment"
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
func TestRemediationCheckoutHTTPWithRealRoles(t *testing.T) {
	app, platform := remediationHTTPDatabase(t)
	ctx := context.Background()
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
	brand := scalar("INSERT INTO brands(slug,name) VALUES('default','Default') RETURNING id")
	userA := scalar("INSERT INTO users(email,password_hash) VALUES('http-a@example.test','test-only-placeholder') RETURNING id")
	userB := scalar("INSERT INTO users(email,password_hash) VALUES('http-b@example.test','test-only-placeholder') RETURNING id")
	tenantA := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Tenant A','a','active') RETURNING id", brand)
	tenantB := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Tenant B','b','active') RETURNING id", brand)
	exec("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner'),($3,$4,'owner')", userA, tenantA, userB, tenantB)
	exec("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'http-a.test','active',now(),'fixture-proof'),($2,'http-b.test','active',now(),'fixture-proof')", tenantA, tenantB)
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", userA)
	q := store.New(platform.Pool)
	for _, v := range []struct {
		token    string
		uid, tid int64
	}{{"http-token-a", userA, tenantA}, {"http-token-b", userB, tenantB}} {
		if _, err := q.CreateSession(ctx, store.CreateSessionParams{ID: middleware.HashSessionToken(v.token), UserID: v.uid, TenantID: pgtype.Int8{Int64: v.tid, Valid: true}, CsrfToken: "test-csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	// QuoteHMACKey must be configured: checkout prices every order through a
	// signed quote, including the legacy plan_code body, which is priced
	// server-side (M5 review 3.2).
	options := api.Options{Logger: zap.NewNop(), Messages: messages, AppPool: app, PlatformPool: platform, Version: "test", Audit: q, BaseURL: "https://panel.example.test", QuoteHMACKey: "test-quote-signing-key-at-least-32-bytes"}
	box, boxErr := secrets.New([]byte(strings.Repeat("b", 32)))
	if boxErr != nil {
		t.Fatal(boxErr)
	}
	tokens, tokenErr := domainMail.NewSuppressionManager([]byte(strings.Repeat("u", 32)))
	if tokenErr != nil {
		t.Fatal(tokenErr)
	}
	options.Secrets = box
	options.MarketingTokens = tokens
	server := httptest.NewServer(api.New(options))
	defer server.Close()
	call := func(base, method, path, token, key string, body any) (int, []byte, http.Header) {
		t.Helper()
		var raw []byte
		if body != nil {
			var err error
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-CSRF-Token", "test-csrf")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		payload, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, payload, resp.Header
	}
	decode := func(raw []byte) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("not one valid JSON document: %q", raw)
		}
		return out
	}
	plan := map[string]any{"code": "http-plan", "name": map[string]string{"en": "HTTP Plan", "zh-CN": "测试套餐"}, "price_cents": 100, "currency": "CNY", "quota_bytes": 1073741824, "mailbox_limit": 5, "domain_limit": 2, "daily_send_limit": 100, "features": map[string]any{}, "active": true}
	if status, raw, _ := call(server.URL, "POST", "/api/v1/platform/plans", "http-token-a", "", plan); status != 200 {
		t.Fatalf("create plan status=%d body=%s", status, raw)
	}
	for _, path := range []string{"/api/v1/billing/plans", "/api/v1/platform/plans"} {
		status, raw, _ := call(server.URL, "GET", path, "http-token-a", "", nil)
		if status != 200 {
			t.Fatal("plan list failed")
		}
		for _, item := range decode(raw)["plans"].([]any) {
			if _, ok := item.(map[string]any)["name"].(map[string]any); !ok {
				t.Fatal("plan JSONB name was not a JSON object")
			}
		}
	}
	status, raw, _ := call(server.URL, "POST", "/api/v1/billing/checkout", "http-token-a", "manual-order", map[string]string{"plan_code": "http-plan"})
	if status != 201 {
		t.Fatalf("manual checkout status=%d body=%s", status, raw)
	}
	out := decode(raw)
	orderID := int64(out["order"].(map[string]any)["id"].(float64))
	if out["payment_method"] != "manual" {
		t.Fatal("manual order response changed")
	}
	status, raw, _ = call(server.URL, "POST", "/api/v1/billing/checkout", "http-token-a", "manual-order", map[string]string{"plan_code": "http-plan"})
	if status != 201 || int64(decode(raw)["order"].(map[string]any)["id"].(float64)) != orderID {
		t.Fatalf("manual retry did not resume same order: %d %s", status, raw)
	}
	// A different tenant may use the same client key without seeing A's order.
	status, raw, _ = call(server.URL, "POST", "/api/v1/billing/checkout", "http-token-b", "manual-order", map[string]string{"plan_code": "http-plan"})
	if status != 201 || int64(decode(raw)["order"].(map[string]any)["id"].(float64)) == orderID {
		t.Fatal("idempotency key crossed tenant boundary")
	}
	paidPath := "/api/v1/platform/orders/" + strconv.FormatInt(orderID, 10) + "/paid"
	for i := 0; i < 2; i++ {
		status, raw, _ = call(server.URL, "POST", paidPath, "http-token-a", "", nil)
		if status != 200 {
			t.Fatalf("manual settlement retry %d: %d %s", i, status, raw)
		}
	}
	if scalar("SELECT count(*) FROM invoices WHERE tenant_id=$1", tenantA) != 1 {
		t.Fatal("manual settlement duplicated invoice")
	}
	invoiceID := scalar("SELECT id FROM invoices WHERE tenant_id=$1", tenantA)
	pdfPath := "/api/v1/billing/invoices/" + strconv.FormatInt(invoiceID, 10) + "/pdf"
	status, raw, headers := call(server.URL, "GET", pdfPath, "http-token-a", "", nil)
	if status != 200 || !bytes.HasPrefix(raw, []byte("%PDF-")) || !strings.Contains(headers.Get("Content-Type"), "application/pdf") {
		t.Fatalf("invoice PDF invalid: status=%d content-type=%s", status, headers.Get("Content-Type"))
	}
	if !strings.HasPrefix(headers.Get("Content-Disposition"), "attachment;") {
		t.Fatal("invoice download missing attachment disposition")
	}
	if status, _, _ := call(server.URL, "GET", pdfPath, "http-token-b", "", nil); status != 404 {
		t.Fatal("invoice PDF crossed tenant boundary")
	}
	if status, _, _ := call(server.URL, "DELETE", "/api/v1/platform/plans/http-plan", "http-token-a", "", nil); status != 409 {
		t.Fatal("referenced plan deletion did not conflict")
	}
	plan["active"] = false
	if status, raw, _ := call(server.URL, "POST", "/api/v1/platform/plans", "http-token-a", "", plan); status != 200 {
		t.Fatalf("plan disable: %d %s", status, raw)
	}
	before := scalar("SELECT count(*) FROM orders WHERE tenant_id=$1", tenantA)
	if status, _, _ := call(server.URL, "POST", "/api/v1/billing/checkout", "http-token-a", "inactive-plan", map[string]string{"plan_code": "http-plan"}); status != 404 {
		t.Fatal("inactive plan remained available for new checkout")
	}
	if before != scalar("SELECT count(*) FROM orders WHERE tenant_id=$1", tenantA) {
		t.Fatal("inactive checkout persisted an order")
	}
	plan["active"] = true
	if status, raw, _ := call(server.URL, "POST", "/api/v1/platform/plans", "http-token-a", "", plan); status != 200 {
		t.Fatalf("plan reactivate: %d %s", status, raw)
	}
	if status, _, _ := call(server.URL, "POST", "/api/v1/platform/brand-hosts", "http-token-b", "", map[string]any{"hostname": "http-brand.example.test", "brand_id": brand}); status != 403 {
		t.Fatal("tenant owner authorized a brand host")
	}
	if status, _, _ := call(server.URL, "POST", "/api/v1/platform/brand-hosts", "http-token-a", "", map[string]any{"hostname": "http-brand.example.test", "brand_id": brand}); status != 200 {
		t.Fatal("platform host authorization failed")
	}

	var fail atomic.Bool
	fail.Store(true)
	var providerCalls atomic.Int32
	var firstKey string
	var keyMu sync.Mutex
	standin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/checkout/sessions" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		providerCalls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		id, _ := strconv.ParseInt(r.Form.Get("metadata[order_id]"), 10, 64)
		if scalar("SELECT count(*) FROM orders WHERE id=$1 AND status='pending'", id) != 1 {
			t.Error("provider contacted before order commit")
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			t.Error("provider idempotency key missing")
		}
		keyMu.Lock()
		if firstKey == "" {
			firstKey = key
		} else if firstKey != key {
			t.Error("retry changed provider idempotency key")
		}
		keyMu.Unlock()
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"stand-in failure"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"test_checkout_session","url":"https://checkout.stripe.com/test-local-stand-in"}`))
	}))
	defer standin.Close()
	stripe := payment.NewStripe("local-test-key", "local-webhook-secret")
	stripe.CheckoutLimits = map[string]payment.AmountLimit{"CNY": {MinCents: 1, MaxCents: 99999999, SettlementCurrency: "CNY", Source: "merchant_contract"}}
	stripe.Base = standin.URL
	stripe.Client = standin.Client()
	options.Payment = stripe
	stripeServer := httptest.NewServer(api.New(options))
	defer stripeServer.Close()
	status, raw, _ = call(stripeServer.URL, "POST", "/api/v1/billing/checkout", "http-token-a", "stripe-retry", map[string]string{"plan_code": "http-plan"})
	if status != 502 {
		t.Fatalf("provider failure status=%d body=%s", status, raw)
	}
	failed := decode(raw)
	pendingID := int64(failed["order_id"].(float64))
	if scalar("SELECT count(*) FROM orders WHERE id=$1 AND status='pending'", pendingID) != 1 {
		t.Fatal("provider failure lost recoverable pending order")
	}
	fail.Store(false)
	status, raw, _ = call(stripeServer.URL, "POST", "/api/v1/billing/checkout", "http-token-a", "stripe-retry", map[string]string{"plan_code": "http-plan"})
	if status != 202 {
		t.Fatalf("provider retry status=%d body=%s", status, raw)
	}
	completed := decode(raw)
	if int64(completed["order"].(map[string]any)["id"].(float64)) != pendingID || completed["checkout_url"] != nil || completed["checkout_state"] != "uncertain" || providerCalls.Load() != 1 {
		t.Fatal("uncertain checkout retried external creation or lost its original order")
	}
	// A realistic completed event: payment_status present and amount_total
	// equal to the order. A MINIMAL event without payment_status is treated
	// as not-paid by design (M5 review 3.3), so the fixture must not rely on
	// the old fail-open behavior.
	event := []byte(fmt.Sprintf(`{"id":"evt_http_completed","type":"checkout.session.completed","data":{"object":{"id":"cs_http_completed","payment_intent":"pi_http_completed","metadata":{"order_id":"%d"},"payment_status":"paid","amount_total":100,"currency":"cny"}}}`, pendingID))
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("local-webhook-secret"))
	_, _ = mac.Write([]byte(stamp + "."))
	_, _ = mac.Write(event)
	signature := "t=" + stamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
	for i := 0; i < 2; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, stripeServer.URL+"/api/v1/billing/webhook/stripe", bytes.NewReader(event))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Stripe-Signature", signature)
		resp, err := stripeServer.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("webhook replay status=%d", resp.StatusCode)
		}
	}
	if got := scalar("SELECT count(*) FROM invoices WHERE tenant_id=$1", tenantA); got != 2 {
		t.Fatalf("provider callback replay duplicated invoice: invoice count=%d", got)
	}
	// The same HTTP-only smoke can seed private accounts for a browser run.
	python, err := osexec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required for the reusable HTTP smoke")
	}
	fixturePath := filepath.Join(t.TempDir(), "browser-fixture.json")
	command := osexec.CommandContext(ctx, python, "../../scripts/remediation-http-smoke.py", "--base-url", server.URL, "--disposable", "--fixture-out", fixturePath)
	command.Env = append(os.Environ(), "URMAIL_ACCEPTANCE_TOKEN=http-token-a", "URMAIL_ACCEPTANCE_CSRF=test-csrf")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("HTTP smoke failed: %v %s", err, output)
	}
	info, err := os.Stat(fixturePath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("browser account fixture is not private")
	}
	t.Log("PASS: real HTTP router + real platform/app roles; manual/Stripe stand-in retries, invoice isolation, inactive plans and brand-host authorization; reusable HTTP/browser-fixture script")
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
	exec(`INSERT INTO plans(code,name,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features) VALUES('browser-marketing','{"en":"Browser paid plan","zh-CN":"浏览器付费套餐"}',100,'CNY',1073741824,5,2,1000,'{"marketing_daily_recipient_limit":3,"marketing_rate_per_minute":600}')`)
	exec("INSERT INTO plan_prices(plan_code,currency,billing_cycle,amount_cents) VALUES('browser-marketing','CNY','monthly',100)")
	exec("INSERT INTO coupons(code,kind,value,max_redemptions) VALUES('browser-full-discount','percent',100,1)")
	browserFixture["billing"] = map[string]any{"plan_code": "browser-marketing", "plan_name": "浏览器付费套餐", "coupon_code": "browser-full-discount"}
	grantTx, grantErr := platform.PlatformTx(ctx)
	if grantErr != nil {
		t.Fatal(grantErr)
	}
	if _, grantErr = grantTx.Exec(ctx, "UPDATE tenants SET status='active' WHERE id=$1", browserTenant); grantErr == nil {
		_, grantErr = grantTx.Exec(ctx, "UPDATE subscriptions SET status='canceled' WHERE tenant_id=$1 AND status IN('active','trialing','past_due')", browserTenant)
	}
	if grantErr == nil {
		_, grantErr = grantTx.Exec(ctx, "INSERT INTO subscriptions(tenant_id,plan_id,status,period_start,period_end,plan_snapshot) SELECT $1,code,'active',now(),now()+interval '1 month',to_jsonb(p) FROM plans p WHERE code='browser-marketing'", browserTenant)
	}
	if grantErr != nil {
		_ = grantTx.Rollback(ctx)
		t.Fatal(grantErr)
	}
	if grantErr = grantTx.Commit(ctx); grantErr != nil {
		t.Fatal(grantErr)
	}
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
		"zero_order_paid_once":             scalar("SELECT count(*) FROM orders WHERE tenant_id=$1 AND plan_code='browser-marketing' AND amount_cents=0 AND status='paid'", browserTenant),
		"zero_order_no_cash":               scalar("SELECT CASE WHEN count(*)=0 THEN 1 ELSE 0 END FROM payments WHERE tenant_id=$1", browserTenant),
		"zero_order_no_commission":         scalar("SELECT CASE WHEN count(*)=0 THEN 1 ELSE 0 END FROM commissions WHERE tenant_id=$1", browserTenant),
		"zero_order_term_once":             scalar("SELECT count(*) FROM subscription_terms t JOIN orders o ON o.id=t.order_id AND o.tenant_id=t.tenant_id WHERE t.tenant_id=$1 AND o.plan_code='browser-marketing' AND o.amount_cents=0 AND o.status='paid'", browserTenant),
		"zero_order_invoice_once":          scalar("SELECT count(*) FROM invoices i JOIN orders o ON o.id=i.order_id AND o.tenant_id=i.tenant_id WHERE i.tenant_id=$1 AND o.plan_code='browser-marketing' AND o.amount_cents=0 AND o.status='paid'", browserTenant),
		"zero_order_coupon_once":           scalar("SELECT count(*) FROM coupon_redemptions WHERE tenant_id=$1 AND coupon_code='browser-full-discount'", browserTenant),
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

// TestRemediationBrowserWithRealRoles isolates browser closure from independently
// evolving payment fixtures. Entitlements are a finite, explicit platform grant.
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
	if _, err = q.CreateSession(ctx, store.CreateSessionParams{ID: middleware.HashSessionToken("http-token-a"), UserID: staff, TenantID: pgtype.Int8{Int64: staffTenant, Valid: true}, CsrfToken: "test-csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); err != nil {
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
