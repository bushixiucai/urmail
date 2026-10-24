package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestBillingV2ContinuousRenewalAndFrozenQuote(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := context.Background()
	var brand, tenant, user int64
	if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('v2','V2') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'V2','v2','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('v2@example.test','unused') RETURNING id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		fmt.Sprintf("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(%d,%d,'owner')", user, tenant),
		"INSERT INTO plans(code,name,description,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('v2','{\"en\":\"Original\"}','{}',10000,'CNY',1073741824,5,2,100)",
		"INSERT INTO plan_prices(plan_code,currency,billing_cycle,amount_cents) VALUES('v2','CNY','monthly',10000)",
	} {
		if _, err := platform.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	seedVerifiedBillingDomain(t, platform, tenant)
	h := NewBillingHandler(store.New(app.Pool))
	h.Pool = platform
	h.QuoteHMACKey = []byte("a-v2-test-key-with-at-least-thirty-two-bytes")
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tenant); c.Set(middleware.ContextUserID, user) }, middleware.RequireTenantTx(app, zap.NewNop()))
	r.POST("/quote", h.Quote)
	r.POST("/checkout", h.Checkout)
	request := func(path, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		r.ServeHTTP(rec, req)
		return rec
	}
	quote := request("/quote", `{"plan_code":"v2","billing_cycle":"monthly"}`, "")
	if quote.Code != 200 {
		t.Fatalf("quote: %d %s", quote.Code, quote.Body.String())
	}
	// Neither current catalog edits nor arbitrary client data may change a confirmed quote.
	if _, err := platform.Exec(ctx, "UPDATE plans SET price_cents=20000,quota_bytes=2147483648,name='{\"en\":\"Edited\"}' WHERE code='v2'"); err != nil {
		t.Fatal(err)
	}
	checkout := request("/checkout", `{"quote":`+quote.Body.String()+`}`, "first")
	if checkout.Code != 201 {
		t.Fatalf("checkout: %d %s", checkout.Code, checkout.Body.String())
	}
	var payload struct {
		Order store.Orders `json:"order"`
	}
	if err := json.Unmarshal(checkout.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	stale := request("/checkout", `{"quote":`+quote.Body.String()+`}`, "second-before-first-paid")
	if stale.Code != 201 {
		t.Fatalf("stale intent: %d %s", stale.Code, stale.Body.String())
	}
	var staleOrder struct {
		Order store.Orders `json:"order"`
	}
	if err := json.Unmarshal(stale.Body.Bytes(), &staleOrder); err != nil {
		t.Fatal(err)
	}
	if err := SettleOrder(ctx, platform, payload.Order.ID); err != nil {
		t.Fatal(err)
	}
	if err := SettleOrderWithDetails(ctx, platform, staleOrder.Order.ID, SettleOptions{Provider: "stripe", ProviderTxnID: "stale-confirmed-txn", EventID: "stale-event", AmountCents: staleOrder.Order.AmountCents, Currency: "CNY", RawPayload: []byte(`{"verified":true}`)}); err != nil {
		t.Fatal(err)
	}
	var reviewCount, paidCount int
	if err := platform.QueryRow(ctx, "SELECT count(*) FROM payment_manual_reviews WHERE order_id=$1 AND status='open'", staleOrder.Order.ID).Scan(&reviewCount); err != nil || reviewCount != 1 {
		t.Fatalf("missing receipt review %d %v", reviewCount, err)
	}
	if err := platform.QueryRow(ctx, "SELECT count(*) FROM subscription_terms WHERE tenant_id=$1", tenant).Scan(&paidCount); err != nil || paidCount != 1 {
		t.Fatalf("stale intent changed rights %d %v", paidCount, err)
	}
	// A verified late receipt on a canceled order is retained without fulfillment.
	if _, err := platform.Exec(ctx, "UPDATE orders SET status='canceled' WHERE id=$1", staleOrder.Order.ID); err != nil {
		t.Fatal(err)
	}
	if err := SettleOrderWithDetails(ctx, platform, staleOrder.Order.ID, SettleOptions{Provider: "stripe", ProviderTxnID: "stale-confirmed-txn", EventID: "late-event", AmountCents: staleOrder.Order.AmountCents, Currency: "CNY", RawPayload: []byte(`{"late":true}`)}); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "SELECT count(*) FROM payment_manual_reviews WHERE order_id=$1", staleOrder.Order.ID).Scan(&reviewCount); err != nil || reviewCount != 1 {
		t.Fatalf("duplicate review %d %v", reviewCount, err)
	}
	var allowed bool
	var start, end time.Time
	if err := platform.QueryRow(ctx, "SELECT urmail_mail_allowed($1)", tenant).Scan(&allowed); err != nil || !allowed {
		t.Fatalf("first paid disabled mail: %v %v", allowed, err)
	}
	if err := platform.QueryRow(ctx, "SELECT period_start,period_end FROM subscriptions WHERE tenant_id=$1 AND status='active'", tenant).Scan(&start, &end); err != nil {
		t.Fatal(err)
	}
	if start.After(time.Now()) || end.After(time.Now().AddDate(1, 0, 0)) {
		t.Fatalf("invalid paid period %s..%s", start, end)
	}
	var quota int64
	var name string
	if err := platform.QueryRow(ctx, "SELECT quota_bytes,name->>'en' FROM urmail_effective_plan($1)", tenant).Scan(&quota, &name); err != nil || quota != 1073741824 || name != "Original" {
		t.Fatalf("snapshot changed quota=%d name=%s err=%v", quota, name, err)
	}
	renewed := request("/quote", `{"plan_code":"v2","billing_cycle":"monthly"}`, "")
	if renewed.Code != 200 {
		t.Fatalf("renew quote %d %s", renewed.Code, renewed.Body.String())
	}
	checkRenew := request("/checkout", `{"quote":`+renewed.Body.String()+`}`, "renew")
	if checkRenew.Code != 201 {
		t.Fatalf("renew checkout %d %s", checkRenew.Code, checkRenew.Body.String())
	}
	if err := json.Unmarshal(checkRenew.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if err := SettleOrder(ctx, platform, payload.Order.ID); err != nil {
		t.Fatal(err)
	}
	var renewedStart, renewedEnd time.Time
	if err := platform.QueryRow(ctx, "SELECT period_start,period_end FROM subscriptions WHERE tenant_id=$1 AND status='active'", tenant).Scan(&renewedStart, &renewedEnd); err != nil {
		t.Fatal(err)
	}
	if !renewedStart.Equal(start) || !renewedEnd.After(end) {
		t.Fatalf("renewal interrupted current period: %s..%s", renewedStart, renewedEnd)
	}
	// A committed intent remains recoverable after both quote expiry and catalog withdrawal.
	var oldQuote billing.Quote
	if err := json.Unmarshal(renewed.Body.Bytes(), &oldQuote); err != nil {
		t.Fatal(err)
	}
	oldQuote.GeneratedAt = time.Now().Add(-2 * time.Hour)
	oldQuote.ExpiresAt = time.Now().Add(-time.Hour)
	oldQuote.Digest = oldQuote.ComputeDigest(h.QuoteHMACKey)
	raw, _ := json.Marshal(oldQuote)
	q := store.New(platform.Pool)
	if err := q.SaveBillingQuote(ctx, store.SaveBillingQuoteParams{Digest: oldQuote.Digest, TenantID: tenant, Payload: raw, PlanSnapshot: []byte(`{}`), ExpiresAt: pgtype.Timestamptz{Time: oldQuote.ExpiresAt, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Exec(ctx, "UPDATE orders SET quote_digest=$1 WHERE id=$2", oldQuote.Digest, payload.Order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Exec(ctx, "UPDATE plans SET active=false WHERE code='v2'"); err != nil {
		t.Fatal(err)
	}
	resume := request("/checkout", `{"quote":`+string(raw)+`}`, "renew")
	if resume.Code != 201 {
		t.Fatalf("committed expired intent not recovered: %d %s", resume.Code, resume.Body.String())
	}
	if err := json.Unmarshal(resume.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Order.Status != "paid" {
		t.Fatalf("resume lost terminal state: %s", resume.Body.String())
	}
}

func TestBillingV2UpgradeUsesFrozenNominalBasis(t *testing.T) {
	p, a := authIntegrationDatabase(t)
	ctx := context.Background()
	var brand, tenant, user int64
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('up','Upgrade') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Up','up','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('up@example.test','unused') RETURNING id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		fmt.Sprintf("INSERT INTO user_tenant_roles VALUES(%d,%d,'owner')", user, tenant),
		`INSERT INTO plans(code,name,description,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features) VALUES('up-old','{}','{}',10000,'CNY',1000000,2,2,100,'{"archive":true}'),('up-new','{}','{}',20000,'CNY',2000000,4,4,200,'{"archive":true,"upgrade_from":["up-old"]}'),('up-bad','{}','{}',30000,'CNY',3000000,5,5,300,'{"archive":false,"upgrade_from":["up-old"]}')`,
		`INSERT INTO plan_prices(plan_code,currency,billing_cycle,amount_cents) VALUES('up-old','CNY','monthly',10000),('up-new','CNY','monthly',20000),('up-bad','CNY','monthly',30000)`,
		`INSERT INTO coupons(code,kind,value) VALUES('upgrade-original-discount','percent',20)`,
	} {
		if _, err := p.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	seedVerifiedBillingDomain(t, p, tenant)
	h := NewBillingHandler(store.New(a.Pool))
	h.Pool = p
	h.QuoteHMACKey = []byte("upgrade-test-key-with-more-than-thirty-two-bytes")
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tenant); c.Set(middleware.ContextUserID, user) }, middleware.RequireTenantTx(a, zap.NewNop()))
	r.POST("/quote", h.Quote)
	r.POST("/checkout", h.Checkout)
	request := func(path, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		r.ServeHTTP(w, req)
		return w
	}
	first := request("/quote", `{"plan_code":"up-old","billing_cycle":"monthly","coupon_code":"upgrade-original-discount"}`, "")
	if first.Code != 200 {
		t.Fatalf("quote %d %s", first.Code, first.Body.String())
	}
	var firstQuote billing.Quote
	if err := json.Unmarshal(first.Body.Bytes(), &firstQuote); err != nil {
		t.Fatal(err)
	}
	if firstQuote.FinalAmountCents != 8000 {
		t.Fatalf("discounted amount %d", firstQuote.FinalAmountCents)
	}
	checkout := request("/checkout", `{"quote":`+first.Body.String()+`}`, "initial-up")
	if checkout.Code != 201 {
		t.Fatalf("checkout %d %s", checkout.Code, checkout.Body.String())
	}
	var payload struct {
		Order store.Orders `json:"order"`
	}
	if err := json.Unmarshal(checkout.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if err := SettleOrder(ctx, p, payload.Order.ID); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-15 * 24 * time.Hour)
	end := start.Add(30 * 24 * time.Hour)
	if _, err := p.Exec(ctx, "UPDATE subscription_terms SET period_start=$2,billing_period_start=$2,period_end=$3 WHERE tenant_id=$1", tenant, start, end); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "UPDATE subscriptions SET period_start=$2,period_end=$3 WHERE tenant_id=$1 AND status='active'", tenant, start, end); err != nil {
		t.Fatal(err)
	}
	bad := request("/quote", `{"plan_code":"up-bad","billing_cycle":"monthly"}`, "")
	if bad.Code != 409 {
		t.Fatalf("lost capability accepted: %d %s", bad.Code, bad.Body.String())
	}
	upgrade := request("/quote", `{"plan_code":"up-new","billing_cycle":"monthly"}`, "")
	if upgrade.Code != 200 {
		t.Fatalf("upgrade %d %s", upgrade.Code, upgrade.Body.String())
	}
	var quote billing.Quote
	if err := json.Unmarshal(upgrade.Body.Bytes(), &quote); err != nil {
		t.Fatal(err)
	}
	if quote.FinalAmountCents != 5000 || quote.Kind != "upgrade" {
		t.Fatalf("upgrade did not preserve nominal basis: %+v", quote)
	}
	paid := request("/checkout", `{"quote":`+upgrade.Body.String()+`}`, "upgrade-once")
	if paid.Code != 201 {
		t.Fatalf("upgrade checkout %d %s", paid.Code, paid.Body.String())
	}
	if err := json.Unmarshal(paid.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if err := SettleOrder(ctx, p, payload.Order.ID); err != nil {
		t.Fatal(err)
	}
	if err := SettleOrder(ctx, p, payload.Order.ID); err != nil {
		t.Fatal(err)
	}
	var periodEnd time.Time
	var plan string
	var adjustmentCount int
	if err := p.QueryRow(ctx, "SELECT period_end,plan_id FROM subscriptions WHERE tenant_id=$1 AND status='active'", tenant).Scan(&periodEnd, &plan); err != nil {
		t.Fatal(err)
	}
	if !periodEnd.Equal(end) || plan != "up-new" {
		t.Fatalf("upgrade changed end or wrong plan: %s %s", periodEnd, plan)
	}
	if err := p.QueryRow(ctx, "SELECT count(*) FROM subscription_terms WHERE tenant_id=$1 AND basis_order_id IS NOT NULL", tenant).Scan(&adjustmentCount); err != nil || adjustmentCount != 1 {
		t.Fatalf("duplicate/missing adjustment %d %v", adjustmentCount, err)
	}
}

func TestBillingV2QuotaCalendarDoesNotResetOnTimezoneChange(t *testing.T) {
	p, _ := authIntegrationDatabase(t)
	ctx := context.Background()
	var brand, tid int64
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('calendar','Calendar') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Calendar','calendar','active') RETURNING id", brand).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	var day1, day2 time.Time
	var boundary1, boundary2 time.Time
	if err := p.QueryRow(ctx, "SELECT urmail_quota_day($1)", tid).Scan(&day1); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "SELECT ends_at FROM tenant_quota_calendar WHERE tenant_id=$1", tid).Scan(&boundary1); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',$1,'business_timezone','\"America/Los_Angeles\"')", tid); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "SELECT urmail_quota_day($1)", tid).Scan(&day2); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "SELECT ends_at FROM tenant_quota_calendar WHERE tenant_id=$1", tid).Scan(&boundary2); err != nil {
		t.Fatal(err)
	}
	if !day1.Equal(day2) || !boundary1.Equal(boundary2) {
		t.Fatalf("timezone change reset live quota window %s/%s %s/%s", day1, day2, boundary1, boundary2)
	}
}

// seedVerifiedBillingDomain represents an already completed ownership proof in financial fixtures.
func seedVerifiedBillingDomain(t *testing.T, p *db.Pool, tenant int64) {
	t.Helper()
	if _, err := p.Exec(context.Background(), "INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'billing-proof.test','active',now(),'fixture-proof')", tenant); err != nil {
		t.Fatal(err)
	}
}
