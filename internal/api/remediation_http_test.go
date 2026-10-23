package api_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

func remediationHTTPDatabase(t *testing.T) (*db.Pool, *db.Pool) {
	t.Helper()
	dsn := os.Getenv("URMAIL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("URMAIL_TEST_DATABASE_URL is required for remediation HTTP integration")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	if config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1" {
		t.Fatal("integration fixture requires loopback PostgreSQL")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("urmail_http_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	var app, platform *db.Pool
	t.Cleanup(func() {
		if app != nil {
			app.Close()
		}
		if platform != nil {
			platform.Close()
		}
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop own HTTP fixture: %v", err)
		}
		_ = admin.Close(ctx)
	})
	target, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	target.Path = "/" + name
	// Cluster roles are shared even though these fixtures own different DBs.
	// Serialize bootstrap on the original database connection, not the new DB.
	if _, err = admin.Exec(context.Background(), "SELECT pg_advisory_lock(7750360020)"); err != nil {
		t.Fatal(err)
	}
	bootstrapErr := db.Bootstrap(context.Background(), target.String())
	_, unlockErr := admin.Exec(context.Background(), "SELECT pg_advisory_unlock(7750360020)")
	if bootstrapErr != nil {
		t.Fatal(bootstrapErr)
	}
	if unlockErr != nil {
		t.Fatal(unlockErr)
	}
	migrationURL := *target
	params := migrationURL.Query()
	params.Set("role", "urmail_migrate")
	migrationURL.RawQuery = params.Encode()
	migrator, err := db.NewMigrator(migrationURL.String())
	if err != nil {
		t.Fatal(err)
	}
	err = migrator.Up(ctx)
	_ = migrator.Close()
	if err != nil {
		t.Fatal(err)
	}
	app, err = db.OpenApp(ctx, target.String())
	if err != nil {
		t.Fatal(err)
	}
	platform, err = db.OpenPlatform(ctx, target.String())
	if err != nil {
		t.Fatal(err)
	}
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
	options := api.Options{Logger: zap.NewNop(), Messages: messages, AppPool: app, PlatformPool: platform, Version: "test", Audit: q, BaseURL: "https://panel.example.test"}
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
	if status != 201 {
		t.Fatalf("provider retry status=%d body=%s", status, raw)
	}
	completed := decode(raw)
	if int64(completed["order"].(map[string]any)["id"].(float64)) != pendingID || completed["checkout_url"] == nil || providerCalls.Load() != 2 {
		t.Fatal("provider retry did not resume the original order")
	}
	event := []byte(fmt.Sprintf(`{"type":"checkout.session.completed","data":{"object":{"metadata":{"order_id":"%d"}}}}`, pendingID))
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
	if scalar("SELECT count(*) FROM invoices WHERE tenant_id=$1", tenantA) != 2 {
		t.Fatal("provider callback replay duplicated invoice")
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
