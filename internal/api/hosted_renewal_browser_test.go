package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/billingrenew"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// Only the provider below is simulated. Login, sessions, roles, CSRF, paid quote,
// hosted intent/binding/consent APIs and the production router are unmodified.
type hostedBrowserProvider struct {
	mu                        sync.Mutex
	requests                  map[string]billingrenew.HostedAuthorizationRequest
	phaseFile                 string
	starts, verifies, charges int
}

func (*hostedBrowserProvider) Name() string { return "stripe" }
func (*hostedBrowserProvider) VerifyMerchant(context.Context, payment.Merchant) (billingrenew.MerchantEvidence, error) {
	return billingrenew.MerchantEvidence{Reference: "explicit isolated browser provider stand-in", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (p *hostedBrowserProvider) StartAuthorization(_ context.Context, _ payment.Merchant, r billingrenew.HostedAuthorizationRequest) (billingrenew.HostedAuthorizationSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.starts++
	ref := "cs_test_" + strings.ReplaceAll(r.RequestID, "-", "")
	p.requests[ref] = r
	return billingrenew.HostedAuthorizationSession{Reference: ref, URL: "https://checkout.stripe.com/c/pay/" + ref, ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (p *hostedBrowserProvider) LookupAuthorization(_ context.Context, _ payment.Merchant, r billingrenew.HostedAuthorizationRequest) (billingrenew.HostedAuthorizationSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for ref, original := range p.requests {
		if original.RequestID == r.RequestID {
			return billingrenew.HostedAuthorizationSession{Reference: ref, URL: "https://checkout.stripe.com/c/pay/" + ref, ExpiresAt: time.Now().Add(time.Hour)}, nil
		}
	}
	return billingrenew.HostedAuthorizationSession{}, billingrenew.ErrHostedAuthorizationUncertain
}
func (p *hostedBrowserProvider) VerifyAuthorization(_ context.Context, _ payment.Merchant, ref string) (billingrenew.VerifiedAuthorization, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.verifies++
	r, ok := p.requests[ref]
	phase, _ := os.ReadFile(p.phaseFile)
	if !ok || strings.TrimSpace(string(phase)) != "completed" {
		return billingrenew.VerifiedAuthorization{}, errors.New("explicit provider stand-in not completed")
	}
	return billingrenew.VerifiedAuthorization{TenantID: r.TenantID, SubscriptionID: r.SubscriptionID, ActorID: r.ActorID, RequestID: r.RequestID, Reference: "cus_browser|pm_" + strings.ReplaceAll(r.RequestID, "-", ""), Label: "Isolated test card •••• 4242", Currency: r.Currency, ExpiresAt: time.Now().AddDate(1, 0, 0)}, nil
}
func (p *hostedBrowserProvider) Charge(context.Context, billingrenew.ChargeRequest) (billingrenew.ChargeOutcome, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.charges++
	return billingrenew.ChargeOutcome{Status: billingrenew.ChargeUncertain}, errors.New("browser fixture never charges")
}
func (*hostedBrowserProvider) Query(context.Context, billingrenew.ChargeRequest) (billingrenew.ChargeOutcome, error) {
	return billingrenew.ChargeOutcome{Status: billingrenew.ChargeUncertain}, nil
}

func TestHostedRenewalBrowserFixture(t *testing.T) {
	dir := os.Getenv("URMAIL_HOSTED_RENEWAL_BROWSER_DIR")
	if dir == "" {
		t.Skip("private production-router browser fixture not requested")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	platform, app := testdb.OpenMigrated(t)
	ctx := t.Context()
	password := "Synthetic-hosted-browser-owner!42"
	hash, e := panelpassword.Hash(password)
	if e != nil {
		t.Fatal(e)
	}
	scalar := func(query string, args ...any) int64 {
		var id int64
		if err := platform.QueryRow(ctx, query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('default','Hosted browser fixture') RETURNING id")
	actor := scalar("INSERT INTO users(email,password_hash) VALUES('hosted-owner@example.test',$1) RETURNING id", hash)
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Hosted browser','hosted-browser','active') RETURNING id", brand)
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", []any{actor, tenant}},
		{"INSERT INTO platform_admins(user_id) VALUES($1)", []any{actor}},
		{"INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'hosted-browser.test','active',now(),'isolated')", []any{tenant}},
		{`INSERT INTO plans(code,name,description,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features) VALUES('hosted-paid','{"en":"Hosted paid plan","zh-CN":"授权测试套餐"}','{}',20000,'CNY',104857600,5,2,200,'{}'); INSERT INTO plan_prices(plan_code,currency,billing_cycle,amount_cents) VALUES('hosted-paid','CNY','monthly',20000)`, nil},
	} {
		if _, e = platform.Exec(ctx, statement.sql, statement.args...); e != nil {
			t.Fatal(e)
		}
	}
	box, e := secrets.New(bytes.Repeat([]byte{42}, 32))
	if e != nil {
		t.Fatal(e)
	}
	encrypted, e := box.EncryptString(`{"secret_key":"sk_test_browser_standin","webhook_secret":"whsec_browser_standin"}`)
	if e != nil {
		t.Fatal(e)
	}
	q := store.New(platform)
	merchant, e := q.PlatformCreatePaymentMerchant(ctx, store.PlatformCreatePaymentMerchantParams{Provider: "stripe", Environment: "sandbox", AccountID: "acct_hostedbrowser", Name: "Explicit browser provider stand-in", Enabled: true, Currencies: []string{"CNY"}, Entrypoint: "hosted", EncryptedConfig: encrypted, CheckoutLimits: []byte(`{}`)})
	if e != nil {
		t.Fatal(e)
	}
	phase := filepath.Join(dir, "provider-phase")
	if e = os.WriteFile(phase, []byte("pending"), 0600); e != nil {
		t.Fatal(e)
	}
	provider := &hostedBrowserProvider{requests: map[string]billingrenew.HostedAuthorizationRequest{}, phaseFile: phase}
	registry := &payment.Registry{Store: q, Secrets: box, BaseURL: "https://billing.platform.test"}
	dispatcher := &billingrenew.Dispatcher{Pool: platform, Registry: registry, Secrets: box, Adapters: map[string]billingrenew.RecurringAdapter{"isolated-hosted-browser-v1": provider}}
	if e = dispatcher.ApproveMerchant(ctx, merchant.ID, "isolated-hosted-browser-v1"); e != nil {
		t.Fatal(e)
	}
	messages, e := i18n.NewStore()
	if e != nil {
		t.Fatal(e)
	}
	quoteKey := "hosted-browser-frozen-quote-key-at-least-32-bytes"
	engine := New(Options{PlatformPool: platform, AppPool: app, Logger: zap.NewNop(), Messages: messages, Secrets: box, BaseURL: registry.BaseURL, QuoteHMACKey: quoteKey, Recurring: dispatcher, WorkersHeld: true})
	server := httptest.NewServer(engine)
	defer server.Close()
	call := func(method, path, token, csrf string, body any, key string) map[string]any {
		raw, _ := json.Marshal(body)
		request, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		if csrf != "" {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		out := map[string]any{}
		if err = json.NewDecoder(response.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode >= 300 {
			t.Fatalf("production fixture %s %d %v", path, response.StatusCode, out)
		}
		return out
	}
	login := call("POST", "/api/v1/auth/login", "", "", map[string]string{"email": "hosted-owner@example.test", "password": password}, "")
	token, csrf := login["token"].(string), login["csrf_token"].(string)
	quote := call("POST", "/api/v1/billing/quote", token, csrf, map[string]string{"plan_code": "hosted-paid", "billing_cycle": "monthly"}, "")
	checkout := call("POST", "/api/v1/billing/checkout", token, csrf, map[string]any{"quote": quote}, "hosted-browser-initial-paid")
	order := checkout["order"].(map[string]any)
	// Seed the paid term through the actual settlement function; no external
	// checkout or funds exist in this private fixture.
	if e = handlers.SettleOrder(ctx, platform, int64(order["id"].(float64))); e != nil {
		t.Fatal(e)
	}
	overview := call("GET", "/api/v1/billing/auto-renewal", token, "", nil, "")
	if overview["preview"] == nil {
		t.Fatal("paid frozen preview absent", overview)
	}
	fixture, _ := json.Marshal(map[string]any{"url": server.URL, "email": "hosted-owner@example.test", "password": password, "merchant_id": merchant.ID, "provider_phase_file": phase, "provider": "explicit in-process provider stand-in; production router/login/CSRF/PostgreSQL are real", "hosted_external_link": "https://checkout.stripe.com (assert safe href only; no real card enrollment)", "tenant_id": tenant})
	if e = os.WriteFile(filepath.Join(dir, "fixture.json"), fixture, 0600); e != nil {
		t.Fatal(e)
	}
	t.Log("Private hosted browser fixture ready; credentials are stored only in fixture.json")
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		if result, err := os.ReadFile(filepath.Join(dir, "browser.done")); err == nil {
			if strings.TrimSpace(string(result)) != "PASS" {
				t.Fatal("browser reported failure")
			}
			provider.mu.Lock()
			charges := provider.charges
			provider.mu.Unlock()
			if charges != 0 {
				t.Fatal("browser fixture attempted a charge")
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	t.Fatal(fmt.Errorf("hosted browser fixture timed out"))
}
