package refund_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/payment"
	refundService "github.com/bushixiucai/urmail/internal/refund"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

type refundFixture struct {
	p, a     *db.Pool
	tid, uid int64
	r        *gin.Engine
}

func newRefundFixture(t *testing.T) *refundFixture {
	t.Helper()
	p, a := testdb.OpenMigrated(t)
	f := &refundFixture{p: p, a: a}
	ctx := context.Background()
	var brand int64
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('refund','Refund') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('refund@example.test','unused') RETURNING id").Scan(&f.uid); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status,owner_user_id) VALUES($1,'Refund','refund','active',$2) RETURNING id", brand, f.uid).Scan(&f.tid); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{fmt.Sprintf("INSERT INTO user_tenant_roles VALUES(%d,%d,'owner')", f.uid, f.tid), fmt.Sprintf("INSERT INTO platform_admins(user_id) VALUES(%d)", f.uid), `INSERT INTO plans(code,name,description,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('refund-plan','{}','{}',10000,'CNY',1073741824,5,2,100)`, `INSERT INTO plan_prices(plan_code,currency,billing_cycle,amount_cents) VALUES('refund-plan','CNY','monthly',10000)`} {
		if _, err := p.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Exec(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'refund-proof.test','active',now(),'fixture-proof')", f.tid); err != nil {
		t.Fatal(err)
	}
	h := handlers.NewBillingHandler(store.New(a))
	h.Pool = p
	h.QuoteHMACKey = []byte("refund-fixture-key-with-at-least-thirty-two-bytes")
	f.r = gin.New()
	f.r.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, f.tid); c.Set(middleware.ContextUserID, f.uid) }, middleware.RequireTenantTx(a, zap.NewNop()))
	f.r.POST("/quote", h.Quote)
	f.r.POST("/checkout", h.Checkout)
	return f
}
func (f *refundFixture) paid(t *testing.T, key string) (store.Orders, int64) {
	t.Helper()
	post := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		f.r.ServeHTTP(w, r)
		return w
	}
	quote := post("/quote", `{"plan_code":"refund-plan","billing_cycle":"monthly"}`)
	if quote.Code != 200 {
		t.Fatalf("quote %d %s", quote.Code, quote.Body.String())
	}
	checkout := post("/checkout", `{"quote":`+quote.Body.String()+`}`)
	if checkout.Code != 201 {
		t.Fatalf("checkout %d %s", checkout.Code, checkout.Body.String())
	}
	var data struct {
		Order store.Orders `json:"order"`
	}
	if err := json.Unmarshal(checkout.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if err := handlers.SettleOrderWithDetails(context.Background(), f.p, data.Order.ID, handlers.SettleOptions{Provider: payment.ProviderStripe, ProviderTxnID: fmt.Sprintf("pi_%d", data.Order.ID), AmountCents: data.Order.AmountCents, Currency: "CNY"}); err != nil {
		t.Fatal(err)
	}
	var receipt int64
	if err := f.p.QueryRow(context.Background(), "SELECT id FROM payments WHERE order_id=$1", data.Order.ID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	return data.Order, receipt
}
func (f *refundFixture) approved(t *testing.T, order store.Orders, receipt int64, number, action string, effective time.Time) store.Refunds {
	t.Helper()
	ctx := context.Background()
	tx, err := f.a.UserTx(ctx, f.tid, f.uid)
	if err != nil {
		t.Fatal(err)
	}
	row, err := store.New(tx).CreateRefund(ctx, store.CreateRefundParams{TenantID: f.tid, OrderID: order.ID, PaymentID: receipt, RefundNo: number, AmountCents: order.AmountCents, Currency: "CNY", Reason: "Requested in integration fixture"})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	target := pgtype.Int8{}
	when := pgtype.Timestamptz{}
	if action != "none" {
		target = pgtype.Int8{Int64: order.ID, Valid: true}
		when = pgtype.Timestamptz{Time: effective, Valid: true}
	}
	row, err = store.New(f.p).PlatformApproveRefund(ctx, store.PlatformApproveRefundParams{RefundID: row.ID, ApprovedAmount: order.AmountCents, ApprovedBy: pgtype.Int8{Int64: f.uid, Valid: true}, ApprovalReason: pgtype.Text{String: "Explicit fixture approval with entitlement decision", Valid: true}, EntitlementAction: action, EntitlementEffectiveAt: when, TargetTermOrderID: target})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestFutureRefundWaitsForSuccessAndEffectiveTime(t *testing.T) {
	f := newRefundFixture(t)
	ctx := context.Background()
	first, _ := f.paid(t, "first")
	future, receipt := f.paid(t, "future")
	var firstEnd, paidEnd time.Time
	if err := f.p.QueryRow(ctx, "SELECT period_end FROM subscription_terms WHERE order_id=$1", first.ID).Scan(&firstEnd); err != nil {
		t.Fatal(err)
	}
	if err := f.p.QueryRow(ctx, "SELECT period_end FROM subscriptions WHERE tenant_id=$1 AND status='active'", f.tid).Scan(&paidEnd); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(3 * time.Second)
	row := f.approved(t, future, receipt, "RF-future", "revoke_future", due)
	var posts atomic.Int32
	var complete atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := "pending"
		if complete.Load() {
			status = "succeeded"
		}
		if r.Method == http.MethodPost {
			posts.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("payment_intent") != fmt.Sprintf("pi_%d", future.ID) || r.Form.Get("amount") != "10000" {
				t.Error("refund lost original payment/amount")
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "re_future", "status": status, "amount": 10000, "currency": "cny", "payment_intent": fmt.Sprintf("pi_%d", future.ID)})
	}))
	defer server.Close()
	adapter := payment.NewStripe("fixture", "fixture")
	adapter.Base = server.URL
	svc := &refundService.Service{Pool: f.p, Enabled: true, ProviderFor: func(context.Context, store.PlatformGetRefundPaymentRow) (payment.Provider, error) {
		return adapter, nil
	}}
	if worked, err := svc.Step(ctx); !worked || err != nil {
		t.Fatalf("dispatch %v %v", worked, err)
	}
	if err := svc.ApplyEntitlements(ctx); err != nil {
		t.Fatal(err)
	}
	var currentEnd time.Time
	if err := f.p.QueryRow(ctx, "SELECT period_end FROM subscriptions WHERE tenant_id=$1 AND status='active'", f.tid).Scan(&currentEnd); err != nil || !currentEnd.Equal(paidEnd) {
		t.Fatal("pending refund removed prepaid rights")
	}
	complete.Store(true)
	if _, err := f.p.Exec(ctx, "UPDATE refunds SET request_started_at=now()-interval '3 minutes' WHERE id=$1", row.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Now().Before(due) {
		if err := svc.ApplyEntitlements(ctx); err != nil {
			t.Fatal(err)
		}
		if err := f.p.QueryRow(ctx, "SELECT period_end FROM subscriptions WHERE tenant_id=$1 AND status='active'", f.tid).Scan(&currentEnd); err != nil || !currentEnd.Equal(paidEnd) {
			t.Fatal("refund acted before approved time")
		}
		time.Sleep(time.Until(due) + 25*time.Millisecond)
	}
	if err := svc.ApplyEntitlements(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyEntitlements(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.p.QueryRow(ctx, "SELECT period_end FROM subscriptions WHERE tenant_id=$1 AND status='active'", f.tid).Scan(&currentEnd); err != nil || !currentEnd.Equal(firstEnd) {
		t.Fatalf("future refund changed current term: %s %s %v", currentEnd, firstEnd, err)
	}
	var allowed bool
	if err := f.p.QueryRow(ctx, "SELECT urmail_mail_allowed($1)", f.tid).Scan(&allowed); err != nil || !allowed {
		t.Fatalf("current mail stopped %v %v", allowed, err)
	}
	if posts.Load() != 1 {
		t.Fatalf("refund repeated %d", posts.Load())
	}
}

func TestLostRefundResultReconcilesWithoutSecondPayout(t *testing.T) {
	f := newRefundFixture(t)
	ctx := context.Background()
	order, receipt := f.paid(t, "lost-result")
	row := f.approved(t, order, receipt, "RF-lost", "none", time.Time{})
	var posts, gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		} else {
			gets.Add(1)
		}
		result := map[string]any{"id": "re_lost", "status": "succeeded", "amount": 10000, "currency": "cny", "payment_intent": fmt.Sprintf("pi_%d", order.ID), "metadata": map[string]string{"refund_no": "RF-lost"}}
		if r.URL.Path == "/v1/refunds" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{result}, "has_more": false})
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	adapter := payment.NewStripe("fixture", "fixture")
	adapter.Base = server.URL
	svc := &refundService.Service{Pool: f.p, Enabled: true, ProviderFor: func(context.Context, store.PlatformGetRefundPaymentRow) (payment.Provider, error) {
		return adapter, nil
	}}
	if _, err := f.p.Exec(ctx, `CREATE FUNCTION reject_refund_result_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='RESULT refund.provider' THEN RAISE EXCEPTION 'injected audit persistence failure'; END IF; RETURN NEW; END$$; CREATE TRIGGER reject_refund_result_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_refund_result_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Step(ctx); err == nil {
		t.Fatal("expected result persistence failure")
	}
	if _, err := f.p.Exec(ctx, "DROP TRIGGER reject_refund_result_audit ON audit_logs"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(ctx, "UPDATE refunds SET request_started_at=now()-interval '3 minutes' WHERE id=$1", row.ID); err != nil {
		t.Fatal(err)
	}
	svc.Quarantined = true
	if worked, _ := svc.Step(ctx); worked {
		t.Fatal("restored snapshot replayed refund")
	}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 1 || gets.Load() != 0 {
		t.Fatal("quarantine contacted provider")
	}
	svc.Quarantined = false
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.p.QueryRow(ctx, "SELECT status FROM refunds WHERE id=$1", row.ID).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("lost refund did not converge: %s %v", status, err)
	}
	if posts.Load() != 1 || gets.Load() < 1 {
		t.Fatalf("reconciliation reissued payout: posts=%d gets=%d", posts.Load(), gets.Load())
	}
}

func TestCurrentRefundKeepsUnrefundedFutureTermsAndMailData(t *testing.T) {
	f := newRefundFixture(t)
	ctx := context.Background()
	current, receipt := f.paid(t, "current-to-refund")
	future, _ := f.paid(t, "future-to-keep")
	var domain int64
	if err := f.p.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'refund-mail.test','active',now(),'owned') RETURNING id", f.tid).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'retained','retained@refund-mail.test','unused',1024,'active')", f.tid, domain); err != nil {
		t.Fatal(err)
	}
	f.approved(t, current, receipt, "RF-current", "terminate_current", time.Now().Add(-time.Minute))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "re_current", "status": "succeeded", "amount": 10000, "currency": "cny", "payment_intent": fmt.Sprintf("pi_%d", current.ID)})
	}))
	defer server.Close()
	adapter := payment.NewStripe("fixture", "fixture")
	adapter.Base = server.URL
	svc := &refundService.Service{Pool: f.p, Enabled: true, ProviderFor: func(context.Context, store.PlatformGetRefundPaymentRow) (payment.Provider, error) {
		return adapter, nil
	}}
	if worked, err := svc.Step(ctx); !worked || err != nil {
		t.Fatalf("dispatch %v %v", worked, err)
	}
	if err := svc.ApplyEntitlements(ctx); err != nil {
		t.Fatal(err)
	}
	var allowed bool
	if err := f.p.QueryRow(ctx, "SELECT urmail_mail_allowed($1)", f.tid).Scan(&allowed); err != nil || allowed {
		t.Fatalf("refunded current period still permits mail %v %v", allowed, err)
	}
	var futureRevoked bool
	var mailboxes int
	if err := f.p.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM subscription_terms WHERE order_id=$1", future.ID).Scan(&futureRevoked); err != nil || futureRevoked {
		t.Fatalf("unrefunded future term was erased %v %v", futureRevoked, err)
	}
	if err := f.p.QueryRow(ctx, "SELECT count(*) FROM mailboxes WHERE tenant_id=$1", f.tid).Scan(&mailboxes); err != nil || mailboxes != 1 {
		t.Fatalf("refund deleted mailbox data %d %v", mailboxes, err)
	}
	// Advance only the fixture's future term to simulate its already-paid start.
	if _, err := f.p.Exec(ctx, "UPDATE subscription_terms SET period_start=now()-interval '1 minute',billing_period_start=now()-interval '1 minute' WHERE order_id=$1", future.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.p.QueryRow(ctx, "SELECT urmail_mail_allowed($1)", f.tid).Scan(&allowed); err != nil || !allowed {
		t.Fatalf("future paid rights did not resume %v %v", allowed, err)
	}
}

func TestPartialRefundsAdjustOnlyTheirSourceCommissionAndPreserveRights(t *testing.T) {
	f := newRefundFixture(t)
	ctx := context.Background()
	order, receipt := f.paid(t, "partial-current")
	var channel int64
	if err := f.p.QueryRow(ctx, "INSERT INTO channels(code,name,commission_bps) VALUES('partial','Partial',1000) RETURNING id").Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(ctx, "INSERT INTO commissions(channel_id,tenant_id,order_id,amount_cents,bps,status,source_payment_id) VALUES($1,$2,$3,1000,1000,'pending',$4)", channel, f.tid, order.ID, receipt); err != nil {
		t.Fatal(err)
	}
	first := order
	first.AmountCents = 2500
	f.approved(t, first, receipt, "RF-partial-one", "none", time.Time{})
	second := order
	second.AmountCents = 7500
	f.approved(t, second, receipt, "RF-partial-two", "none", time.Time{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		n := calls.Add(1)
		amount := r.Form.Get("amount")
		var cents int64
		if _, err := fmt.Sscan(amount, &cents); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("re_partial_%d", n), "status": "succeeded", "amount": cents, "currency": "cny", "payment_intent": fmt.Sprintf("pi_%d", order.ID)})
	}))
	defer server.Close()
	adapter := payment.NewStripe("fixture", "fixture")
	adapter.Base = server.URL
	svc := &refundService.Service{Pool: f.p, Enabled: true, ProviderFor: func(context.Context, store.PlatformGetRefundPaymentRow) (payment.Provider, error) {
		return adapter, nil
	}}
	for range 2 {
		if worked, err := svc.Step(ctx); !worked || err != nil {
			t.Fatalf("dispatch %v %v", worked, err)
		}
	}
	if err := svc.ApplyEntitlements(ctx); err != nil {
		t.Fatal(err)
	}
	var adjusted, revoked, applied int64
	if err := f.p.QueryRow(ctx, "SELECT COALESCE(sum(a.amount_cents),0) FROM commission_refund_adjustments a JOIN commissions c ON c.id=a.commission_id WHERE c.tenant_id=$1", f.tid).Scan(&adjusted); err != nil || adjusted != 1000 {
		t.Fatalf("commission adjustment=%d err=%v", adjusted, err)
	}
	if err := f.p.QueryRow(ctx, "SELECT count(*) FROM subscription_terms WHERE tenant_id=$1 AND revoked_at IS NOT NULL", f.tid).Scan(&revoked); err != nil || revoked != 0 {
		t.Fatalf("partial refunds revoked rights=%d err=%v", revoked, err)
	}
	if err := f.p.QueryRow(ctx, "SELECT count(*) FROM refunds WHERE tenant_id=$1 AND entitlement_applied_at IS NOT NULL", f.tid).Scan(&applied); err != nil || applied != 0 {
		t.Fatalf("none action changed rights=%d err=%v", applied, err)
	}
	if worked, err := svc.Step(ctx); worked || err != nil {
		t.Fatalf("duplicate refund dispatch %v %v", worked, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("provider refund count=%d", calls.Load())
	}
}

func TestFirstTrialAfterConfirmedFullRefundHasItsOwnValidEntitlement(t *testing.T) {
	f := newRefundFixture(t)
	ctx := context.Background()
	order, receipt := f.paid(t, "refunded-before-first-trial")
	f.approved(t, order, receipt, "RF-before-trial", "terminate_current", time.Now().Add(-time.Minute))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "re_before_trial", "status": "succeeded", "amount": 10000, "currency": "cny", "payment_intent": fmt.Sprintf("pi_%d", order.ID)})
	}))
	defer server.Close()
	adapter := payment.NewStripe("fixture", "fixture")
	adapter.Base = server.URL
	svc := &refundService.Service{Pool: f.p, Enabled: true, ProviderFor: func(context.Context, store.PlatformGetRefundPaymentRow) (payment.Provider, error) {
		return adapter, nil
	}}
	if worked, err := svc.Step(ctx); !worked || err != nil {
		t.Fatalf("refund %v %v", worked, err)
	}
	if err := svc.ApplyEntitlements(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(ctx, "UPDATE plans SET features='{\"trial_eligible\":true}' WHERE code='refund-plan'; INSERT INTO coupons(code,kind,value,max_redemptions) VALUES('trial-after-refund','trial_days',3,1)"); err != nil {
		t.Fatal(err)
	}
	var domain int64
	if err := f.p.QueryRow(ctx, "SELECT id FROM domains WHERE tenant_id=$1 ORDER BY id LIMIT 1", f.tid).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	tx, err := f.a.UserTx(ctx, f.tid, f.uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.New(tx).RedeemTenantTrial(ctx, store.RedeemTenantTrialParams{TenantID: f.tid, DomainID: domain, CouponCode: "trial-after-refund", PlanCode: "refund-plan"}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var allowed bool
	if err = f.p.QueryRow(ctx, "SELECT urmail_mail_allowed($1)", f.tid).Scan(&allowed); err != nil || !allowed {
		t.Fatalf("valid protected trial remained blocked by revoked paid history: %v %v", allowed, err)
	}
	if _, err = f.p.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'trial','trial@refund-proof.test','unused',1024,'active')", f.tid, domain); err != nil {
		t.Fatalf("valid trial could not allocate a mailbox: %v", err)
	}
}
