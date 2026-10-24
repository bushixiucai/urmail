package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
)

type merchantHTTPTransport struct{ target *url.URL }

func (r merchantHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	target := *clone.URL
	target.Host = r.target.Host
	target.Scheme = r.target.Scheme
	clone.URL = &target
	return http.DefaultTransport.RoundTrip(clone)
}
func TestMerchantCheckoutWebhookDurabilityAndConfiguration(t *testing.T) {
	p, a := authIntegrationDatabase(t)
	ctx := context.Background()
	var creates atomic.Int32
	var orderID int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/account":
			fmt.Fprint(w, `{"id":"acct_one"}`)
		case "/v1/checkout/sessions":
			creates.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("metadata[order_id]") != strconv.FormatInt(orderID, 10) || r.Form.Get("payment_intent_data[metadata][order_id]") != strconv.FormatInt(orderID, 10) {
				t.Error("PI/session metadata not bound to same order")
			}
			fmt.Fprint(w, `{"id":"cs_local","url":"https://checkout.stripe.com/c/pay/local"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	box, err := secrets.New([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	q := store.New(p.Pool)
	registry := &payment.Registry{Store: q, Secrets: box, BaseURL: "https://panel.example.test", Client: &http.Client{Transport: merchantHTTPTransport{target: target}, Timeout: time.Second}}
	h := &PaymentGatewayHandler{Registry: registry, Platform: q, Pool: p, Secrets: box}
	r := gin.New()
	r.POST("/merchants", h.SaveMerchant)
	r.PATCH("/merchants/:merchant_id", h.SaveMerchant)
	r.GET("/methods", h.Methods)
	r.POST("/payments/:merchant_id/webhook", h.Webhook)
	call := func(method, path, body, signature string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if signature != "" {
			req.Header.Set("Stripe-Signature", signature)
		}
		r.ServeHTTP(w, req)
		return w
	}
	cfg := `{"provider":"stripe","environment":"sandbox","account_id":"acct_one","name":"Stripe test","enabled":true,"currencies":["CNY"],"entrypoint":"hosted","checkout_limits":{"CNY":{"min_cents":1,"max_cents":100000,"settlement_currency":"CNY","limits_source":"merchant_contract"}},"credentials":{"secret_key":"sk_test_local","webhook_secret":"whsec_local"}}`
	response := call(http.MethodPost, "/merchants", cfg, "")
	if response.Code != 201 {
		t.Fatalf("config %d %s", response.Code, response.Body)
	}
	var saved struct {
		Merchant store.PlatformCreatePaymentMerchantRow `json:"merchant"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	id := saved.Merchant.ID
	if strings.Contains(response.Body.String(), "sk_test_local") || strings.Contains(response.Body.String(), "whsec_local") {
		t.Fatal("credentials exposed")
	}
	row, err := q.PlatformGetPaymentMerchant(ctx, id)
	if err != nil || !strings.HasPrefix(row.EncryptedConfig, "enc:v1:") {
		t.Fatalf("unencrypted config %v", err)
	}
	var brand, tenant int64
	if err = p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('gateway','Gateway') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Gateway','gateway','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO plans(code,name,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('gateway','{}',100,'CNY',1073741824,5,2,100)"); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code,billing_cycle) VALUES($1,'GATEWAY1','new','pending',100,'CNY','gateway','monthly') RETURNING id", tenant).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	order, err := q.PlatformGetMerchantOrderByReference(ctx, store.PlatformGetMerchantOrderByReferenceParams{OrderID: orderID})
	if err != nil {
		t.Fatal(err)
	}
	r.POST("/checkout", func(c *gin.Context) { h.Checkout(c, order, id) })
	if _, err = p.Exec(ctx, "UPDATE payment_merchants SET checkout_limits='{}' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	response = call(http.MethodPost, "/checkout", "", "")
	if response.Code != 422 || creates.Load() != 0 {
		t.Fatalf("unconfigured limits created payment: %d %s creates%d", response.Code, response.Body, creates.Load())
	}
	var intents int
	if err = p.QueryRow(ctx, "SELECT count(*) FROM payment_checkout_intents WHERE order_id=$1", orderID).Scan(&intents); err != nil || intents != 0 {
		t.Fatalf("limit rejection persisted a creating intent: %d %v", intents, err)
	}
	if _, err = p.Exec(ctx, `UPDATE payment_merchants SET checkout_limits='{"CNY":{"min_cents":101,"max_cents":100000,"settlement_currency":"CNY","limits_source":"merchant_contract"}}' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	response = call(http.MethodPost, "/checkout", "", "")
	if response.Code != 422 || creates.Load() != 0 {
		t.Fatalf("below-minimum payment created: %d %s", response.Code, response.Body)
	}
	if _, err = p.Exec(ctx, `UPDATE payment_merchants SET checkout_limits='{"CNY":{"min_cents":1,"max_cents":100000,"settlement_currency":"CNY","limits_source":"merchant_contract"}}' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	response = call(http.MethodPost, "/checkout", "", "")
	if response.Code != 201 {
		t.Fatalf("checkout %d %s", response.Code, response.Body)
	}
	response = call(http.MethodPost, "/checkout", "", "")
	if response.Code != 200 || creates.Load() != 1 {
		t.Fatalf("duplicate external checkout count%d %d %s", creates.Load(), response.Code, response.Body)
	}
	payload := fmt.Sprintf(`{"id":"evt_one","livemode":false,"type":"payment_intent.succeeded","data":{"object":{"id":"pi_local","status":"succeeded","amount":100,"currency":"cny","metadata":{"order_id":"%d"}}}}`, orderID)
	sign := func(raw string) string {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte("whsec_local"))
		_, _ = mac.Write([]byte(ts + "." + raw))
		return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
	}
	path := fmt.Sprintf("/payments/%d/webhook", id)
	response = call(http.MethodPost, path, payload, sign(payload))
	if response.Code != 200 {
		t.Fatalf("webhook %d %s", response.Code, response.Body)
	}
	response = call(http.MethodPost, path, payload, sign(payload))
	if response.Code != 200 {
		t.Fatalf("replay %d %s", response.Code, response.Body)
	}
	var count int
	var scope string
	if err = p.QueryRow(ctx, "SELECT count(*),max(account_scope) FROM payments WHERE order_id=$1", orderID).Scan(&count, &scope); err != nil || count != 1 || scope != "stripe:sandbox:acct_one" {
		t.Fatalf("scoped payment count%d scope%s %v", count, scope, err)
	}
	conflict := strings.Replace(payload, `"amount":100`, `"amount":101`, 1)
	response = call(http.MethodPost, path, conflict, sign(conflict))
	if response.Code != 409 {
		t.Fatalf("event payload reuse accepted %d %s", response.Code, response.Body)
	}
	// An in-flight request can hold a stale pending order; terminal/unknown intent states must never expose an old payment link.
	for _, state := range []string{"paid", "cancel_requested", "closed", "uncertain"} {
		if _, err = p.Exec(ctx, "UPDATE payment_checkout_intents SET state=$2 WHERE order_id=$1", orderID, state); err != nil {
			t.Fatal(err)
		}
		response = call(http.MethodPost, "/checkout", "", "")
		if response.Code != 200 || strings.Contains(response.Body.String(), "checkout_url") || strings.Contains(response.Body.String(), "qr_code") {
			t.Fatalf("unsafe checkout recovery state%s %d %s", state, response.Code, response.Body)
		}
		if state == "paid" && !strings.Contains(response.Body.String(), "paid_pending_review") {
			t.Fatalf("known receipt was not signaled: %s", response.Body)
		}
	}
	// Disabling without credentials preserves the previous write-only secret envelope.
	disabled := `{"provider":"stripe","environment":"sandbox","account_id":"acct_one","name":"Disabled","enabled":false,"currencies":["CNY"],"entrypoint":"hosted"}`
	response = call(http.MethodPatch, fmt.Sprintf("/merchants/%d", id), disabled, "")
	if response.Code != 200 {
		t.Fatalf("disable %d %s", response.Code, response.Body)
	}
	response = call(http.MethodGet, "/methods?currency=CNY", "", "")
	if response.Code != 200 || strings.Contains(response.Body.String(), "merchant_id") {
		t.Fatalf("disabled method exposed %d %s", response.Code, response.Body)
	}
	_, _, err = registry.Load(ctx, id, false)
	if err != nil {
		t.Fatalf("disable destroyed credentials: %v", err)
	}
	// The shared app role may not forge receipts or inspect private callback payloads.
	for _, sql := range []string{"UPDATE payments SET amount_cents=1 WHERE tenant_id=$1", "SELECT payload FROM payment_webhook_inbox WHERE merchant_id=$1"} {
		tx, e := a.TenantTx(ctx, tenant)
		if e != nil {
			t.Fatal(e)
		}
		_, e = tx.Exec(ctx, sql, tenant)
		_ = tx.Rollback(ctx)
		if e == nil {
			t.Fatalf("private financial privilege exposed %s", sql)
		}
	}
}

func TestPaymentWebhookWithoutDependenciesIsDisabled(t *testing.T) {
	h := &PaymentGatewayHandler{}
	r := gin.New()
	r.POST("/payments/:merchant_id/webhook", h.Webhook)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/payments/1/webhook", strings.NewReader("{}")))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled webhook %d %s", w.Code, w.Body)
	}
}

func TestPayPalDifferentApprovalEventsShareOneDurableCapture(t *testing.T) {
	p, app := authIntegrationDatabase(t)
	ctx := context.Background()
	box, err := secrets.New([]byte(strings.Repeat("p", 32)))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var captured atomic.Bool
	captureEntered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth2/token":
			fmt.Fprint(w, `{"access_token":"token","expires_in":300}`)
		case "/v1/notifications/verify-webhook-signature":
			fmt.Fprint(w, `{"verification_status":"SUCCESS"}`)
		case "/v2/checkout/orders/PAYPAL1/capture":
			if calls.Add(1) == 1 {
				close(captureEntered)
			}
			<-release
			captured.Store(true)
			fmt.Fprint(w, `{"id":"PAYPAL1","status":"COMPLETED","purchase_units":[{"payments":{"captures":[{"id":"CAPTURE1","status":"COMPLETED","amount":{"value":"1.00","currency_code":"USD"}}]}}]}`)
		case "/v2/checkout/orders/PAYPAL1":
			if captured.Load() {
				fmt.Fprint(w, `{"id":"PAYPAL1","status":"COMPLETED","purchase_units":[{"payee":{"merchant_id":"merchant1"},"amount":{"value":"1.00","currency_code":"USD"},"payments":{"captures":[{"id":"CAPTURE1","status":"COMPLETED","amount":{"value":"1.00","currency_code":"USD"}}]}}]}`)
			} else {
				fmt.Fprint(w, `{"id":"PAYPAL1","status":"APPROVED","purchase_units":[{"payee":{"merchant_id":"merchant1"},"amount":{"value":"1.00","currency_code":"USD"}}]}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	q := store.New(p.Pool)
	encrypted, err := box.EncryptString(`{"client_id":"client","client_secret":"secret","webhook_id":"hook"}`)
	if err != nil {
		t.Fatal(err)
	}
	merchant, err := q.PlatformCreatePaymentMerchant(ctx, store.PlatformCreatePaymentMerchantParams{Provider: payment.ProviderPayPal, Environment: "sandbox", AccountID: "merchant1", Name: "PayPal", Enabled: true, Currencies: []string{"USD"}, Entrypoint: "hosted", EncryptedConfig: encrypted})
	if err != nil {
		t.Fatal(err)
	}
	var brand, tenant, orderID int64
	for _, seed := range []struct {
		sql  string
		dest *int64
	}{{"INSERT INTO brands(slug,name) VALUES('paypal','PayPal') RETURNING id", &brand}} {
		if err = p.QueryRow(ctx, seed.sql).Scan(seed.dest); err != nil {
			t.Fatal(err)
		}
	}
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'PayPal','paypal','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO plans(code,name,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('paypal','{}',100,'USD',1073741824,5,2,100)"); err != nil {
		t.Fatal(err)
	}
	var owner int64
	if err = p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('paypal-owner@fixture.test','unused') RETURNING id").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO user_tenant_roles VALUES($1,$2,'owner')", owner, tenant); err != nil {
		t.Fatal(err)
	}
	seedVerifiedBillingDomain(t, p, tenant)
	billingHandler := NewBillingHandler(store.New(app.Pool))
	billingHandler.Pool = p
	billingHandler.QuoteHMACKey = []byte("paypal-fixture-quote-key-at-least-thirty-two-bytes")
	billingRouter := gin.New()
	billingRouter.Use(func(c *gin.Context) {
		c.Set(middleware.ContextTenantID, tenant)
		c.Set(middleware.ContextUserID, owner)
	}, middleware.RequireTenantTx(app, zap.NewNop()))
	billingRouter.POST("/quote", billingHandler.Quote)
	billingRouter.POST("/checkout", billingHandler.Checkout)
	checkoutCall := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "paypal-capture-formal-order")
		billingRouter.ServeHTTP(w, req)
		return w
	}
	quote := checkoutCall("/quote", `{"plan_code":"paypal","billing_cycle":"monthly","currency":"USD"}`)
	if quote.Code != 200 {
		t.Fatalf("formal quote %d %s", quote.Code, quote.Body)
	}
	checkout := checkoutCall("/checkout", `{"quote":`+quote.Body.String()+`}`)
	if checkout.Code != 201 {
		t.Fatalf("formal checkout %d %s", checkout.Code, checkout.Body)
	}
	var created struct {
		Order store.Orders `json:"order"`
	}
	if err = json.Unmarshal(checkout.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	orderID = created.Order.ID
	orderNo := created.Order.OrderNo

	if _, err = q.PlatformCreateCheckoutIntent(ctx, store.PlatformCreateCheckoutIntentParams{OrderID: orderID, TenantID: tenant, Provider: payment.ProviderPayPal, AccountScope: "paypal:sandbox:merchant1"}); err != nil {
		t.Fatal(err)
	}
	if _, err = q.PlatformSaveMerchantCheckout(ctx, store.PlatformSaveMerchantCheckoutParams{TenantID: tenant, OrderID: orderID, SessionID: pgtype.Text{String: "PAYPAL1", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	h := &PaymentGatewayHandler{Registry: &payment.Registry{Store: q, Secrets: box, BaseURL: "https://panel.example.test", Client: &http.Client{Transport: merchantHTTPTransport{target: target}, Timeout: 5 * time.Second}}, Platform: q, Pool: p, Secrets: box}
	r := gin.New()
	r.POST("/payments/:merchant_id/webhook", h.Webhook)
	call := func(event string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"id":%q,"event_type":"CHECKOUT.ORDER.APPROVED","resource":{"id":"PAYPAL1","status":"APPROVED","purchase_units":[{"custom_id":"%d","reference_id":%q,"payee":{"merchant_id":"merchant1"},"amount":{"currency_code":"USD","value":"1.00"}}]}}`, event, orderID, orderNo)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("/payments/%d/webhook", merchant.ID), strings.NewReader(body))
		for _, header := range []string{"Paypal-Auth-Algo", "Paypal-Cert-Url", "Paypal-Transmission-Id", "Paypal-Transmission-Sig", "Paypal-Transmission-Time"} {
			req.Header.Set(header, "local")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call("APPROVAL1") }()
	select {
	case <-captureEntered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("capture not reached")
	}
	second := call("APPROVAL2")
	close(release)
	first := <-done
	if first.Code != 200 || second.Code != 503 || calls.Load() != 1 {
		t.Fatalf("capture gate: first=%d second=%d physical=%d body=%s", first.Code, second.Code, calls.Load(), first.Body)
	}
	retry := call("APPROVAL2")
	if retry.Code != 200 || calls.Load() != 1 {
		t.Fatalf("approval retry recaptured: %d physical%d %s", retry.Code, calls.Load(), retry.Body)
	}
	var receipts int
	if err = p.QueryRow(ctx, "SELECT count(*) FROM payments WHERE order_id=$1", orderID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("capture receipts %d %v", receipts, err)
	}
	// Captured callbacks may carry only the external related order, not our custom_id.
	capturedEvent := `{"id":"CAPTURE_EVENT","event_type":"PAYMENT.CAPTURE.COMPLETED","resource":{"id":"CAPTURE1","status":"COMPLETED","payee":{"merchant_id":"merchant1"},"amount":{"currency_code":"USD","value":"1.00"},"supplementary_data":{"related_ids":{"order_id":"PAYPAL1"}}}}`
	capturedRequest := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("/payments/%d/webhook", merchant.ID), strings.NewReader(body))
		for _, header := range []string{"Paypal-Auth-Algo", "Paypal-Cert-Url", "Paypal-Transmission-Id", "Paypal-Transmission-Sig", "Paypal-Transmission-Time"} {
			req.Header.Set(header, "local")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	confirmation := capturedRequest(capturedEvent)
	if confirmation.Code != 200 {
		t.Fatalf("external PayPal order was not resolved through intent: %d %s", confirmation.Code, confirmation.Body)
	}
	// Another verified receipt must enter manual review, without extending the paid term twice.
	extra := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(capturedEvent, "CAPTURE_EVENT", "EXTRA_EVENT"), "CAPTURE1", "CAPTURE_EXTRA"), `"1.00"`, `"1.01"`)
	confirmation = capturedRequest(extra)
	if confirmation.Code != 200 {
		t.Fatalf("verified extra cash not recorded: %d %s", confirmation.Code, confirmation.Body)
	}
	var reviews int
	if err = p.QueryRow(ctx, "SELECT count(*) FROM payment_manual_reviews WHERE order_id=$1", orderID).Scan(&reviews); err != nil || reviews != 1 {
		t.Fatalf("extra receipt review %d %v", reviews, err)
	}
}

func TestCheckoutCancelRacePersistsRemoteSessionAndReleasesCoupon(t *testing.T) {
	p, _ := authIntegrationDatabase(t)
	ctx := context.Background()
	box, err := secrets.New([]byte(strings.Repeat("c", 32)))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var closeCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/checkout/sessions":
			close(entered)
			<-release
			fmt.Fprint(w, `{"id":"cs_race","url":"https://checkout.stripe.com/c/pay/race"}`)
		case "/v1/checkout/sessions/cs_race":
			fmt.Fprint(w, `{"id":"cs_race","status":"open","payment_status":"unpaid","amount_total":100,"currency":"cny"}`)
		case "/v1/checkout/sessions/cs_race/expire":
			closeCalls.Add(1)
			fmt.Fprint(w, `{"id":"cs_race","status":"expired"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	q := store.New(p.Pool)
	encrypted, err := box.EncryptString(`{"secret_key":"sk_test_local","webhook_secret":"whsec_local"}`)
	if err != nil {
		t.Fatal(err)
	}
	merchant, err := q.PlatformCreatePaymentMerchant(ctx, store.PlatformCreatePaymentMerchantParams{Provider: payment.ProviderStripe, Environment: "sandbox", AccountID: "acct_cancel", Name: "Stripe", CheckoutLimits: []byte(`{"CNY":{"min_cents":1,"max_cents":100000,"settlement_currency":"CNY","limits_source":"merchant_contract"}}`), Enabled: true, Currencies: []string{"CNY"}, Entrypoint: "hosted", EncryptedConfig: encrypted})
	if err != nil {
		t.Fatal(err)
	}
	var brand, tenant, oid int64
	if err = p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('cancel-race','Cancel') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Cancel','cancel-race','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code,billing_cycle) VALUES($1,'CANCEL_RACE','new','pending',100,'CNY','free','monthly') RETURNING id", tenant).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO coupons(code,kind,value,max_redemptions) VALUES('CANCEL_COUPON','percent',5,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO coupon_redemptions(coupon_code,tenant_id,order_id) VALUES('CANCEL_COUPON',$1,$2)", tenant, oid); err != nil {
		t.Fatal(err)
	}
	order, err := q.PlatformGetMerchantOrderByReference(ctx, store.PlatformGetMerchantOrderByReferenceParams{OrderID: oid})
	if err != nil {
		t.Fatal(err)
	}
	h := &PaymentGatewayHandler{Registry: &payment.Registry{Store: q, Secrets: box, BaseURL: "https://panel.example.test", Client: &http.Client{Transport: merchantHTTPTransport{target: target}, Timeout: 5 * time.Second}}, Platform: q, Pool: p, Secrets: box}
	r := gin.New()
	r.POST("/checkout", func(c *gin.Context) { h.Checkout(c, order, merchant.ID) })
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodPost, "/checkout", nil))
		done <- w
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("checkout not reached")
	}
	_, err = q.PlatformRequestCheckoutCancel(ctx, store.PlatformRequestCheckoutCancelParams{TenantID: tenant, OrderID: oid})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	response := <-done
	if response.Code != 202 {
		t.Fatalf("race returned %d %s", response.Code, response.Body)
	}
	intent, err := q.PlatformGetCheckoutIntent(ctx, store.PlatformGetCheckoutIntentParams{TenantID: tenant, OrderID: oid})
	if err != nil || intent.SessionID.String != "cs_race" || intent.State != "closed" || closeCalls.Load() != 1 {
		t.Fatalf("lost session/cancel state %+v close%d err%v", intent, closeCalls.Load(), err)
	}
	var orderStatus string
	var reserved, used int
	if err = p.QueryRow(ctx, "SELECT status FROM orders WHERE id=$1", oid).Scan(&orderStatus); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "SELECT count(*) FROM coupon_redemptions WHERE coupon_code='CANCEL_COUPON'").Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "SELECT redeemed_count FROM coupons WHERE code='CANCEL_COUPON'").Scan(&used); err != nil {
		t.Fatal(err)
	}
	if orderStatus != "canceled" || reserved != 0 || used != 0 {
		t.Fatalf("cancel state=%s couponrows=%d counter=%d", orderStatus, reserved, used)
	}
}
