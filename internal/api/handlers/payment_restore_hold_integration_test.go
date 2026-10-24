package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestRestorePaymentHoldSurvivesReleaseUntilExplicitDecision(t *testing.T) {
	p, app := authIntegrationDatabase(t)
	ctx := t.Context()
	q := store.New(p.Pool)
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := p.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := p.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('restore-hold','Restore') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Restore','restore-hold','active') RETURNING id", brand)
	owner := scalar("INSERT INTO users(email,password_hash) VALUES('restore-hold@fixture.test','unused') RETURNING id")
	exec("INSERT INTO user_tenant_roles VALUES($1,$2,'owner')", owner, tenant)
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", owner)
	exec("INSERT INTO plans(code,name,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('restore-hold','{}',100,'CNY',1073741824,5,2,100)")
	seedVerifiedBillingDomain(t, p, tenant)
	billing := NewBillingHandler(store.New(app.Pool))
	billing.Pool = p
	billing.QuoteHMACKey = []byte("restore-hold-formal-quote-key-at-least-thirty-two-bytes")
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextTenantID, tenant)
		c.Set(middleware.ContextUserID, owner)
	}, middleware.RequireTenantTx(app, zap.NewNop()))
	router.POST("/quote", billing.Quote)
	router.POST("/checkout", billing.Checkout)
	call := func(path, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		router.ServeHTTP(w, r)
		return w
	}
	create := func(key string) int64 {
		t.Helper()
		quote := call("/quote", `{"plan_code":"restore-hold","billing_cycle":"monthly","currency":"CNY"}`, "")
		if quote.Code != 200 {
			t.Fatalf("quote%d %s", quote.Code, quote.Body)
		}
		created := call("/checkout", `{"quote":`+quote.Body.String()+`}`, key)
		if created.Code != 201 {
			t.Fatalf("checkout%d %s", created.Code, created.Body)
		}
		var result struct {
			Order store.Orders `json:"order"`
		}
		if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Order.ID
	}
	addHeldIntent := func(order int64, session string) {
		t.Helper()
		exec("INSERT INTO payment_checkout_intents(tenant_id,order_id,provider,account_scope,state,session_id,restore_hold,updated_at) VALUES($1,$2,'stripe','legacy','uncertain',$3,true,now()-interval '5 minutes')", tenant, order, session)
	}
	var externalWrites atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			externalWrites.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "cs_restore_closed") {
			fmt.Fprint(w, `{"id":"cs_restore_closed","status":"expired","payment_status":"unpaid","amount_total":100,"currency":"cny"}`)
			return
		}
		fmt.Fprint(w, `{"id":"cs_restore_paid","status":"complete","payment_status":"paid","payment_intent":"pi_restore_paid","amount_total":100,"currency":"cny"}`)
	}))
	defer provider.Close()
	stripe := payment.NewStripe("restore-local-fixture", "fixture-hook")
	stripe.Base = provider.URL
	stripe.Client = provider.Client()
	gateway := &PaymentGatewayHandler{Pool: p, Platform: q, LegacyStripe: stripe, Quarantined: false}
	order := create("restore-held-paid")
	addHeldIntent(order, "cs_restore_paid")
	// Instance quarantine is already released. A normal recovery worker must still
	// import cash only, even though this order has a valid frozen quote and seal.
	if err := gateway.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if scalar("SELECT count(*) FROM subscription_terms WHERE tenant_id=$1 AND order_id=$2", tenant, order) != 0 || scalar("SELECT count(*) FROM orders WHERE id=$1 AND status='pending'", order) != 1 {
		t.Fatal("released quarantine automatically reopened restored subscription")
	}
	if scalar("SELECT count(*) FROM payments WHERE tenant_id=$1 AND order_id=$2", tenant, order) != 1 || scalar("SELECT count(*) FROM payment_manual_reviews WHERE tenant_id=$1 AND order_id=$2 AND status='open'", tenant, order) != 1 || scalar("SELECT count(*) FROM payment_checkout_intents WHERE order_id=$1 AND restore_hold", order) != 1 {
		t.Fatal("cash fact, review or persistent restore hold lost")
	}
	opts := SettleOptions{Provider: payment.ProviderStripe, AccountScope: "legacy", ProviderTxnID: "pi_restore_paid", EventID: "ordinary-callback-after-release", AmountCents: 100, Currency: "CNY"}
	if err := SettleOrderWithDetails(ctx, p, order, opts); err != nil {
		t.Fatal(err)
	}
	if scalar("SELECT count(*) FROM subscription_terms WHERE tenant_id=$1 AND order_id=$2", tenant, order) != 0 {
		t.Fatal("ordinary callback bypassed restore hold")
	}
	queryRouter := gin.New()
	queryRouter.Use(func(c *gin.Context) { c.Set(middleware.ContextUserID, owner) })
	queryRouter.POST("/:id", gateway.QueryLegacy)
	queried := httptest.NewRecorder()
	queryRouter.ServeHTTP(queried, httptest.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("/%d", order), nil))
	if queried.Code != 200 || !strings.Contains(queried.Body.String(), `"receipt_only":true`) || !strings.Contains(queried.Body.String(), `"restore_hold":true`) {
		t.Fatalf("held query DTO%d %s", queried.Code, queried.Body)
	}
	review := scalar("SELECT id FROM payment_manual_reviews WHERE tenant_id=$1 AND order_id=$2", tenant, order)
	// The persistent hold cannot be cleared by a fabricated manual decision or a
	// terminal-close update while this receipt is still outstanding.
	if err := q.PlatformMarkCheckoutClosed(ctx, store.PlatformMarkCheckoutClosedParams{TenantID: tenant, OrderID: order}); err != nil {
		t.Fatal(err)
	}
	invalidDecision := opts
	invalidDecision.ManualReviewID = review
	invalidDecision.EventID = ""
	invalidDecision.ReviewReason = "Untrusted caller cannot release the restore payment hold"
	if err := SettleOrderWithDetails(ctx, p, order, invalidDecision); err == nil {
		t.Fatal("manual decision without platform actor released hold")
	}
	if scalar("SELECT count(*) FROM payment_checkout_intents WHERE order_id=$1 AND restore_hold", order) != 1 {
		t.Fatal("unresolved cash hold cleared without valid decision")
	}
	opts.ManualReviewID = review
	opts.ActorUserID = owner
	opts.ReviewReason = "Operator verified original cash and explicitly restores the paid term"
	opts.AuditAction = "FULFILL receipt.restore_review"
	opts.EventID = ""
	exec("CREATE FUNCTION reject_restore_review_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='FULFILL receipt.restore_review' THEN RAISE EXCEPTION 'injected restore review audit failure'; END IF; RETURN NEW; END $$")
	exec("CREATE TRIGGER reject_restore_review_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_restore_review_audit()")
	if err := SettleOrderWithDetails(ctx, p, order, opts); err == nil {
		t.Fatal("failed audit committed reviewed restoration")
	}
	if scalar("SELECT count(*) FROM payment_checkout_intents WHERE order_id=$1 AND restore_hold", order) != 1 || scalar("SELECT count(*) FROM subscription_terms WHERE tenant_id=$1 AND order_id=$2", tenant, order) != 0 || scalar("SELECT count(*) FROM payment_manual_reviews WHERE id=$1 AND status='open'", review) != 1 {
		t.Fatal("audit failure did not atomically retain hold and unresolved review")
	}
	exec("DROP TRIGGER reject_restore_review_audit ON audit_logs")
	exec("DROP FUNCTION reject_restore_review_audit()")
	if err := SettleOrderWithDetails(ctx, p, order, opts); err != nil {
		t.Fatal(err)
	}
	if scalar("SELECT count(*) FROM subscription_terms WHERE tenant_id=$1 AND order_id=$2", tenant, order) != 1 || scalar("SELECT count(*) FROM payment_checkout_intents WHERE order_id=$1 AND restore_hold", order) != 0 {
		t.Fatal("explicit reviewed fulfillment did not clear hold atomically")
	}
	// Confirmed partial refunds do not release the hold; only a complete original-route refund does.
	refundedOrder := create("restore-held-refund")
	addHeldIntent(refundedOrder, "cs_refund")
	refundOpts := SettleOptions{Provider: payment.ProviderStripe, AccountScope: "legacy", ProviderTxnID: "pi_restore_refund", AmountCents: 100, Currency: "CNY"}
	if err := SettleOrderWithDetails(ctx, p, refundedOrder, refundOpts); err != nil {
		t.Fatal(err)
	}
	receipt := scalar("SELECT id FROM payments WHERE tenant_id=$1 AND order_id=$2", tenant, refundedOrder)
	for part := 1; part <= 2; part++ {
		exec("INSERT INTO refunds(tenant_id,order_id,payment_id,refund_no,amount_cents,currency,status,reason,approved_by,approval_reason) VALUES($1,$2,$3,$4,50,'CNY','completed','Confirmed original-route refund fixture',$5,'Explicit audited platform approval fixture')", tenant, refundedOrder, receipt, fmt.Sprintf("RESTORE-REFUND-%d", part), owner)
		if err := q.PlatformMarkCheckoutRefunded(ctx, store.PlatformMarkCheckoutRefundedParams{TenantID: tenant, OrderID: refundedOrder}); err != nil {
			t.Fatal(err)
		}
		want := int64(1)
		if part == 2 {
			want = 0
		}
		if got := scalar("SELECT count(*) FROM payment_checkout_intents WHERE order_id=$1 AND restore_hold", refundedOrder); got != want {
			t.Fatalf("refund part%d hold%d want%d", part, got, want)
		}
	}
	closedOrder := create("restore-held-close")
	addHeldIntent(closedOrder, "cs_restore_closed")
	if err := gateway.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if scalar("SELECT count(*) FROM payment_checkout_intents WHERE order_id=$1 AND state='closed' AND NOT restore_hold", closedOrder) != 1 || scalar("SELECT count(*) FROM orders WHERE id=$1 AND status='canceled'", closedOrder) != 1 {
		t.Fatal("provider-confirmed terminal close did not clear hold")
	}
	// Approval is not received cash. Restored PayPal approval must not trigger a
	// new capture even when instance quarantine has already been released.
	approvedOrder := create("restore-held-paypal-approval")
	exec("INSERT INTO payment_checkout_intents(tenant_id,order_id,provider,account_scope,state,session_id,restore_hold) VALUES($1,$2,'paypal','paypal:sandbox:restore-account','uncertain','PP-RESTORE',true)", tenant, approvedOrder)
	paypal := payment.NewPayPal("fixture-client", "fixture-secret", "fixture-webhook")
	paypal.Base = provider.URL
	paypal.Client = provider.Client()
	state, code, approvalErr := gateway.processVerifiedEvent(ctx, payment.Merchant{Provider: payment.ProviderPayPal, Environment: "sandbox", AccountID: "restore-account"}, paypal, payment.WebhookEvent{Provider: payment.ProviderPayPal, OrderID: approvedOrder, ProviderTxnID: "PP-RESTORE", EventType: "CHECKOUT.ORDER.APPROVED", AmountCents: 100, Currency: "CNY"}, []byte(`{}`))
	if approvalErr == nil || state != "uncertain" || code != "restore_manual_decision_required" {
		t.Fatalf("restored approval was not held: state=%s code=%s err=%v", state, code, approvalErr)
	}
	if scalar("SELECT count(*) FROM payments WHERE tenant_id=$1 AND order_id=$2", tenant, approvedOrder) != 0 || scalar("SELECT count(*) FROM payment_checkout_intents WHERE order_id=$1 AND restore_hold", approvedOrder) != 1 || scalar("SELECT count(*) FROM operational_alerts WHERE tenant_id=$1 AND alert_key=$2 AND state='firing'", tenant, fmt.Sprintf("payment-checkout:%d", approvedOrder)) != 1 {
		t.Fatal("approval hold lost or fabricated a receipt")
	}
	// A verified terminal VOIDED response needs no nonexistent PayPal close API.
	// A 404 is unknown and must retain the hold and pending order.
	var forbiddenMoneyWrites atomic.Int64
	terminalProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/oauth2/token":
			fmt.Fprint(w, `{"access_token":"fixture-token","expires_in":300}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "VOIDED"):
			fmt.Fprint(w, `{"id":"PP-VOIDED","status":"VOIDED","purchase_units":[{"payee":{"merchant_id":"restore-account"},"amount":{"currency_code":"USD","value":"1.00"}}]}`)
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"name":"RESOURCE_NOT_FOUND"}`)
		default:
			forbiddenMoneyWrites.Add(1)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer terminalProvider.Close()
	target, err := url.Parse(terminalProvider.URL)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New([]byte(strings.Repeat("r", 32)))
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := box.EncryptString(`{"client_id":"fixture","client_secret":"fixture-secret","webhook_id":"fixture-hook"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.PlatformCreatePaymentMerchant(ctx, store.PlatformCreatePaymentMerchantParams{Provider: payment.ProviderPayPal, Environment: "sandbox", AccountID: "restore-account", Name: "Restore terminal fixture", Currencies: []string{"USD"}, Entrypoint: "hosted", EncryptedConfig: cipher}); err != nil {
		t.Fatal(err)
	}
	gateway.Registry = &payment.Registry{Store: q, Secrets: box, BaseURL: "https://panel.example.test", Client: &http.Client{Transport: merchantHTTPTransport{target: target}, Timeout: time.Second}}
	for _, session := range []string{"PP-VOIDED", "PP-UNKNOWN"} {
		terminalOrder := create("restore-terminal-" + session)
		exec("INSERT INTO payment_checkout_intents(tenant_id,order_id,provider,account_scope,state,session_id,restore_hold) VALUES($1,$2,'paypal','paypal:sandbox:restore-account','uncertain',$3,true)", tenant, terminalOrder, session)
		if session == "PP-VOIDED" {
			merchantID := scalar("SELECT id FROM payment_merchants WHERE provider='paypal' AND account_id='restore-account'")
			query := gin.New()
			query.Use(func(c *gin.Context) { c.Set(middleware.ContextUserID, owner) })
			query.POST("/:merchant_id/:order_id", gateway.Query)
			response := httptest.NewRecorder()
			query.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("/%d/%d", merchantID, terminalOrder), nil))
			if response.Code != 200 || !strings.Contains(response.Body.String(), `"restore_hold":false`) {
				t.Fatalf("terminal query%d %s", response.Code, response.Body)
			}
		}
		err = gateway.CancelCheckout(ctx, tenant, terminalOrder)
		if session == "PP-VOIDED" {
			if err != nil || scalar("SELECT count(*) FROM payment_checkout_intents WHERE order_id=$1 AND state='closed' AND NOT restore_hold", terminalOrder) != 1 || scalar("SELECT count(*) FROM orders WHERE id=$1 AND status='canceled'", terminalOrder) != 1 {
				t.Fatalf("confirmed terminal close: %v", err)
			}
		} else if err == nil || scalar("SELECT count(*) FROM payment_checkout_intents WHERE order_id=$1 AND restore_hold", terminalOrder) != 1 || scalar("SELECT count(*) FROM orders WHERE id=$1 AND status='pending'", terminalOrder) != 1 {
			t.Fatal("unknown provider session incorrectly treated as terminal")
		}
	}
	workerTerminal := create("restore-worker-terminal")
	exec("INSERT INTO payment_checkout_intents(tenant_id,order_id,provider,account_scope,state,session_id,restore_hold,updated_at) VALUES($1,$2,'paypal','paypal:sandbox:restore-account','uncertain','PP-WORKER-VOIDED',true,now()-interval '5 minutes')", tenant, workerTerminal)
	if err = gateway.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if scalar("SELECT count(*) FROM payment_checkout_intents WHERE order_id=$1 AND state='closed' AND NOT restore_hold", workerTerminal) != 1 {
		t.Fatal("worker did not converge verified PayPal terminal state")
	}
	if forbiddenMoneyWrites.Load() != 0 {
		t.Fatal("terminal reconciliation called a capture/close money endpoint")
	}
	if externalWrites.Load() != 0 {
		t.Fatal("restore reconciliation requested external money or a new session")
	}
}
