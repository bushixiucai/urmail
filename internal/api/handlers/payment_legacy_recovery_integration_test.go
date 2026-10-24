package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestLegacyStripeRecoveryAndQuarantineQueryNeverCreateOrCapture(t *testing.T) {
	p, _ := authIntegrationDatabase(t)
	ctx := context.Background()
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
	brand := scalar("INSERT INTO brands(slug,name) VALUES('legacy-recovery','Legacy') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Legacy','legacy-recovery','active') RETURNING id", brand)
	actor := scalar("INSERT INTO users(email,password_hash) VALUES('legacy-recovery@fixture.test','unused') RETURNING id")
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", actor)
	exec("INSERT INTO plans(code,name,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('legacy-recovery','{}',100,'CNY',1073741824,5,2,100)")
	var creates, captures, closes atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "cs_legacy_paid"):
			fmt.Fprint(w, `{"id":"cs_legacy_paid","payment_status":"paid","status":"complete","payment_intent":"pi_legacy_paid","amount_total":100,"currency":"cny"}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "cs_legacy_cancel"):
			fmt.Fprint(w, `{"id":"cs_legacy_cancel","payment_status":"unpaid","status":"open","amount_total":100,"currency":"cny"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "cs_legacy_cancel/expire"):
			closes.Add(1)
			fmt.Fprint(w, `{"id":"cs_legacy_cancel","status":"expired"}`)
		default:
			if strings.Contains(r.URL.Path, "capture") {
				captures.Add(1)
			} else {
				creates.Add(1)
			}
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	stripe := payment.NewStripe("fixture-legacy-key", "fixture-legacy-webhook")
	stripe.Base = srv.URL
	stripe.Client = srv.Client()
	h := &PaymentGatewayHandler{Pool: p, Platform: q, LegacyStripe: stripe, Quarantined: true}
	order := scalar("INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code) VALUES($1,'LEGACY-PAID','new','pending',100,'CNY','legacy-recovery') RETURNING id", tenant)
	exec("INSERT INTO payment_checkout_intents(tenant_id,order_id,provider,account_scope,state,session_id,updated_at) VALUES($1,$2,'stripe','legacy','uncertain','cs_legacy_paid',now()-interval '5 minutes')", tenant, order)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.ContextUserID, actor) })
	r.POST("/api/v1/platform/orders/:id/query", h.QueryLegacy)
	for range 2 {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("/api/v1/platform/orders/%d/query", order), nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"receipt_only":true`) {
			t.Fatalf("legacy receipt-only query %d %s", w.Code, w.Body)
		}
	}
	if scalar("SELECT count(*) FROM payments WHERE tenant_id=$1 AND order_id=$2", tenant, order) != 1 || scalar("SELECT count(*) FROM payment_manual_reviews WHERE tenant_id=$1 AND order_id=$2", tenant, order) != 1 || scalar("SELECT count(*) FROM subscription_terms WHERE tenant_id=$1 AND order_id=$2", tenant, order) != 0 {
		t.Fatal("legacy reconciliation duplicated cash or fulfilled during quarantine")
	}
	missing := scalar("INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code) VALUES($1,'LEGACY-MISSING','new','pending',100,'CNY','legacy-recovery') RETURNING id", tenant)
	cancel := scalar("INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code) VALUES($1,'LEGACY-CANCEL','new','pending',100,'CNY','legacy-recovery') RETURNING id", tenant)
	exec("INSERT INTO payment_checkout_intents(tenant_id,order_id,provider,account_scope,state,session_id,updated_at) VALUES($1,$2,'stripe','legacy','uncertain',NULL,now()-interval '5 minutes'),($1,$3,'stripe','legacy','cancel_requested','cs_legacy_cancel',now()-interval '5 minutes')", tenant, missing, cancel)
	h.Quarantined = false
	if err := h.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if scalar("SELECT count(*) FROM operational_alerts WHERE alert_key=$1 AND state='firing'", fmt.Sprintf("payment-checkout:%d", missing)) != 1 {
		t.Fatal("missing legacy session was not flagged")
	}
	if scalar("SELECT count(*) FROM orders WHERE id=$1 AND status='canceled'", cancel) != 1 || closes.Load() != 1 {
		t.Fatal("legacy cancellation did not close the original session and converge")
	}
	if creates.Load() != 0 || captures.Load() != 0 {
		t.Fatalf("legacy recovery created/captured external payment: %d/%d", creates.Load(), captures.Load())
	}
}
