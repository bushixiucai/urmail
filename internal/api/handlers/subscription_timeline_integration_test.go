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
	"github.com/bushixiucai/urmail/internal/store"
)

type billingScenario struct {
	p, a     *db.Pool
	tid, uid int64
	r        *gin.Engine
}

func newBillingScenario(t *testing.T) *billingScenario {
	t.Helper()
	p, a := authIntegrationDatabase(t)
	ctx := context.Background()
	s := &billingScenario{p: p, a: a}
	var brand int64
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('timeline','Timeline') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Timeline','timeline','active') RETURNING id", brand).Scan(&s.tid); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('timeline@example.test','unused') RETURNING id").Scan(&s.uid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO user_tenant_roles VALUES($1,$2,'owner')", s.uid, s.tid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO plans(code,name,description,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features) VALUES('high','{}','{}',20000,'CNY',2000,5,2,200,'{"archive":true}'),('low','{}','{}',10000,'CNY',1000,5,2,100,'{"archive":false,"downgrade_from":["high"]}'); INSERT INTO plan_prices(plan_code,currency,billing_cycle,amount_cents) VALUES('high','CNY','monthly',20000),('low','CNY','monthly',10000);`); err != nil {
		t.Fatal(err)
	}
	seedVerifiedBillingDomain(t, p, s.tid)
	h := NewBillingHandler(store.New(a))
	h.Pool = p
	h.QuoteHMACKey = []byte("timeline-fixture-key-with-at-least-thirty-two-bytes")
	s.r = gin.New()
	s.r.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, s.tid); c.Set(middleware.ContextUserID, s.uid) }, middleware.RequireTenantTx(a, zap.NewNop()))
	s.r.POST("/quote", h.Quote)
	s.r.POST("/checkout", h.Checkout)
	s.r.POST("/change", h.ScheduleChange)
	s.r.POST("/change/cancel", h.CancelScheduledChange)
	s.r.POST("/cancel", h.CancelSubscription)
	return s
}
func (s *billingScenario) post(t *testing.T, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	s.r.ServeHTTP(w, req)
	return w
}
func (s *billingScenario) pay(t *testing.T, plan, key string) store.Orders {
	t.Helper()
	quote := s.post(t, "/quote", `{"plan_code":"`+plan+`","billing_cycle":"monthly"}`, "")
	if quote.Code != 200 {
		t.Fatalf("quote %d %s", quote.Code, quote.Body.String())
	}
	w := s.post(t, "/checkout", `{"quote":`+quote.Body.String()+`}`, key)
	if w.Code != 201 {
		t.Fatalf("checkout %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Order store.Orders `json:"order"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if err := SettleOrder(context.Background(), s.p, result.Order.ID); err != nil {
		t.Fatal(err)
	}
	return result.Order
}
func TestScheduledDowngradeUsesAllocatedQuotaAndPreservesPrepaidTimeline(t *testing.T) {
	s := newBillingScenario(t)
	ctx := context.Background()
	first := s.pay(t, "high", "high-current")
	var did int64
	if err := s.p.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'timeline.test','active',now(),'owned') RETURNING id", s.tid).Scan(&did); err != nil {
		t.Fatal(err)
	}
	if _, err := s.p.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'owner','owner@timeline.test','unused',1200,'active')", s.tid, did); err != nil {
		t.Fatal(err)
	}
	if _, err := s.p.Exec(ctx, "UPDATE mailboxes SET bytes_used=400,usage_measured_at=now() WHERE tenant_id=$1", s.tid); err != nil {
		t.Fatal(err)
	}
	rejected := s.post(t, "/change", `{"plan_code":"low"}`, "")
	if rejected.Code != 409 || !strings.Contains(rejected.Body.String(), "PLAN_CAPACITY_EXCEEDED") {
		t.Fatalf("allocated quota not enforced: %d %s", rejected.Code, rejected.Body.String())
	}
	if _, err := s.p.Exec(ctx, "UPDATE mailboxes SET quota_bytes=800 WHERE tenant_id=$1", s.tid); err != nil {
		t.Fatal(err)
	}
	scheduled := s.post(t, "/change", `{"plan_code":"low"}`, "")
	if scheduled.Code != 200 {
		t.Fatalf("schedule %d %s", scheduled.Code, scheduled.Body.String())
	}
	changed := s.pay(t, "low", "low-next")
	if changed.Kind != "downgrade" {
		t.Fatalf("unexpected order kind %s", changed.Kind)
	}
	tx, err := s.a.UserTx(ctx, s.tid, s.uid)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := store.New(tx).GetEffectivePlan(ctx, s.tid)
	_ = tx.Rollback(ctx)
	if err != nil || effective.Code != "high" {
		t.Fatalf("early downgrade: %s %v", effective.Code, err)
	}
	third := s.pay(t, "low", "low-another")
	if third.Kind != "renew" {
		t.Fatalf("future plan continuation not renewal: %s", third.Kind)
	}
	canceled := s.post(t, "/cancel", `{}`, "")
	if canceled.Code != 200 {
		t.Fatalf("cancel %d %s", canceled.Code, canceled.Body.String())
	}
	var lastEnd, summaryEnd time.Time
	var flag bool
	var bases int
	if err := s.p.QueryRow(ctx, "SELECT period_end FROM subscription_terms WHERE order_id=$1", third.ID).Scan(&lastEnd); err != nil {
		t.Fatal(err)
	}
	if err := s.p.QueryRow(ctx, "SELECT period_end,cancel_at_period_end FROM subscriptions WHERE tenant_id=$1 AND status='active'", s.tid).Scan(&summaryEnd, &flag); err != nil || !flag || !summaryEnd.Equal(lastEnd) {
		t.Fatalf("cancel swallowed prepaid terms: %v %s/%s %v", flag, summaryEnd, lastEnd, err)
	}
	if err := s.p.QueryRow(ctx, "SELECT count(*) FROM subscription_terms WHERE tenant_id=$1 AND basis_order_id IS NULL AND revoked_at IS NULL", s.tid).Scan(&bases); err != nil || bases != 3 {
		t.Fatalf("lost paid base terms %d %v", bases, err)
	}
	var firstEnd, nextStart time.Time
	if err := s.p.QueryRow(ctx, "SELECT period_end FROM subscription_terms WHERE order_id=$1", first.ID).Scan(&firstEnd); err != nil {
		t.Fatal(err)
	}
	if err := s.p.QueryRow(ctx, "SELECT period_start FROM subscription_terms WHERE order_id=$1", changed.ID).Scan(&nextStart); err != nil || !nextStart.Equal(firstEnd) {
		t.Fatalf("discontinuous next plan %s/%s %v", nextStart, firstEnd, err)
	}
}
func TestZeroAmountCheckoutHasNoReceiptOrRefundableFunds(t *testing.T) {
	s := newBillingScenario(t)
	ctx := context.Background()
	if _, err := s.p.Exec(ctx, "INSERT INTO coupons(code,kind,value) VALUES('zero-coupon','percent',100)"); err != nil {
		t.Fatal(err)
	}
	quote := s.post(t, "/quote", `{"plan_code":"high","billing_cycle":"monthly","coupon_code":"zero-coupon"}`, "")
	if quote.Code != 200 {
		t.Fatalf("quote %d %s", quote.Code, quote.Body.String())
	}
	checkout := s.post(t, "/checkout", `{"quote":`+quote.Body.String()+`}`, "zero-intent")
	if checkout.Code != 201 {
		t.Fatalf("zero checkout %d %s", checkout.Code, checkout.Body.String())
	}
	var result struct {
		Order store.Orders `json:"order"`
	}
	if err := json.Unmarshal(checkout.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var receipts int
	var source pgtype.Int8
	if err := s.p.QueryRow(ctx, "SELECT count(*) FROM payments WHERE order_id=$1", result.Order.ID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("zero order fabricated cash: %d %v", receipts, err)
	}
	if err := s.p.QueryRow(ctx, "SELECT source_payment_id FROM invoices WHERE order_id=$1", result.Order.ID).Scan(&source); err != nil || source.Valid {
		t.Fatalf("zero invoice bound a receipt: %+v %v", source, err)
	}
	if result.Order.Status != "paid" {
		t.Fatalf("zero order did not settle: %+v", result.Order)
	}
	tx, err := s.a.UserTx(ctx, s.tid, s.uid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = store.New(tx).CreateRefund(ctx, store.CreateRefundParams{TenantID: s.tid, OrderID: result.Order.ID, PaymentID: 99999, RefundNo: fmt.Sprintf("zero-%d", result.Order.ID), AmountCents: 1, Currency: "CNY", Reason: "No actual receipt exists"})
	if err == nil {
		t.Fatal("zero order became refundable")
	}
}

func TestScheduledIntentionCanBeCanceledOnlyBeforeFunding(t *testing.T) {
	s := newBillingScenario(t)
	s.pay(t, "high", "intention-current")
	if w := s.post(t, "/change", `{"plan_code":"low"}`, ""); w.Code != 200 {
		t.Fatalf("schedule %d %s", w.Code, w.Body.String())
	}
	if w := s.post(t, "/change/cancel", `{}`, ""); w.Code != 200 {
		t.Fatalf("cancel intention %d %s", w.Code, w.Body.String())
	}
	if w := s.post(t, "/change", `{"plan_code":"low"}`, ""); w.Code != 200 {
		t.Fatalf("reschedule %d %s", w.Code, w.Body.String())
	}
	s.pay(t, "low", "funded-future")
	w := s.post(t, "/change/cancel", `{}`, "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "PLAN_CHANGE_ALREADY_PAID") {
		t.Fatalf("funded intention canceled %d %s", w.Code, w.Body.String())
	}
}

func TestFutureDowngradeActivationBlocksOverAllocationWithoutExtendingOldRights(t *testing.T) {
	s := newBillingScenario(t)
	ctx := context.Background()
	current := s.pay(t, "high", "capacity-current")
	if w := s.post(t, "/change", `{"plan_code":"low"}`, ""); w.Code != 200 {
		t.Fatalf("schedule %d %s", w.Code, w.Body.String())
	}
	future := s.pay(t, "low", "capacity-future")
	var domain int64
	if err := s.p.QueryRow(ctx, "SELECT id FROM domains WHERE tenant_id=$1 ORDER BY id LIMIT 1", s.tid).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if _, err := s.p.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,usage_measured_at) VALUES($1,$2,'capacity','capacity@billing-proof.test','unused',1200,'active',now())", s.tid, domain); err != nil {
		t.Fatal(err)
	}
	var allowed bool
	if err := s.p.QueryRow(ctx, "SELECT urmail_mail_allowed($1)", s.tid).Scan(&allowed); err != nil || !allowed {
		t.Fatalf("current higher plan was prematurely blocked %v %v", allowed, err)
	}
	// Shift only the isolated fixture's ledger dates to exercise the activation
	// boundary without waiting a month or adding a production clock override.
	if _, err := s.p.Exec(ctx, "UPDATE subscription_terms SET period_start=now()-interval '1 month',billing_period_start=now()-interval '1 month',period_end=now()-interval '1 minute' WHERE order_id=$1", current.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.p.Exec(ctx, "UPDATE subscription_terms SET period_start=now()-interval '1 minute',billing_period_start=now()-interval '1 minute',period_end=now()+interval '1 month' WHERE order_id=$1", future.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.p.QueryRow(ctx, "SELECT urmail_mail_allowed($1)", s.tid).Scan(&allowed); err != nil || allowed {
		t.Fatalf("overallocated lower paid plan allowed mail %v %v", allowed, err)
	}
	var effective string
	if err := s.p.QueryRow(ctx, "SELECT code FROM urmail_effective_plan($1)", s.tid).Scan(&effective); err != nil || effective != "low" {
		t.Fatalf("old rights extended: %s %v", effective, err)
	}
	if _, err := s.p.Exec(ctx, "UPDATE mailboxes SET quota_bytes=800 WHERE tenant_id=$1", s.tid); err != nil {
		t.Fatal(err)
	}
	if err := s.p.QueryRow(ctx, "SELECT urmail_mail_allowed($1)", s.tid).Scan(&allowed); err != nil || !allowed {
		t.Fatalf("cleanup did not restore fitting paid rights %v %v", allowed, err)
	}
}
