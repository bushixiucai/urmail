package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

// Raw tenant-role persistence is not evidence of a server-priced purchase.
// A compromised app-side query must not turn an unsigned zero order into rights.
func TestUnsealedRawZeroOrderCannotGainPaidEntitlement(t *testing.T) {
	s := newBillingScenario(t)
	ctx := t.Context()
	tx, err := s.a.UserTx(ctx, s.tid, s.uid)
	if err != nil {
		t.Fatal(err)
	}
	order, err := store.New(tx).CreateOrder(ctx, store.CreateOrderParams{TenantID: s.tid, OrderNo: "RAW-UNSEALED-ZERO", Kind: "new", Status: "pending", AmountCents: 0, Currency: "CNY", PlanCode: pgtype.Text{String: "high", Valid: true}, IdempotencyKey: pgtype.Text{String: "raw-unsealed-zero", Valid: true}, BillingCycle: "monthly"})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Log("raw unsigned order correctly refused by DB")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	response := s.post(t, "/checkout", `{"plan_code":"high","billing_cycle":"monthly"}`, "raw-unsealed-zero")
	var plan string
	if err = s.p.QueryRow(ctx, "SELECT code FROM urmail_effective_plan($1)", s.tid).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if plan != "free" {
		t.Fatalf("unsealed raw app-role order granted paid rights: order=%d status=%d effective_plan=%s", order.ID, response.Code, plan)
	}
}

func TestRawPositiveOrderCannotCreatePhysicalCheckoutOnRecovery(t *testing.T) {
	s := newBillingScenario(t)
	ctx := t.Context()
	tx, err := s.a.UserTx(ctx, s.tid, s.uid)
	if err != nil {
		t.Fatal(err)
	}
	order, err := store.New(tx).CreateOrder(ctx, store.CreateOrderParams{TenantID: s.tid, OrderNo: "RAW-ONE-CENT", Kind: "new", Status: "pending", AmountCents: 1, Currency: "CNY", PlanCode: pgtype.Text{String: "high", Valid: true}, IdempotencyKey: pgtype.Text{String: "raw-one-cent", Valid: true}, BillingCycle: "monthly"})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_forbidden","url":"https://invalid.example/checkout"}`))
	}))
	defer upstream.Close()
	h := NewBillingHandler(store.New(s.a))
	h.Pool = s.p
	h.QuoteHMACKey = []byte("timeline-fixture-key-with-at-least-thirty-two-bytes")
	h.Stripe = payment.NewStripe("offline-key", "")
	h.Stripe.Base = upstream.URL
	h.Stripe.Client = upstream.Client()
	h.Stripe.CheckoutLimits = map[string]payment.AmountLimit{"CNY": {MinCents: 1, MaxCents: 1000000}}
	s.r.POST("/boundary-checkout", h.Checkout)
	response := s.post(t, "/boundary-checkout", `{"plan_code":"high","billing_cycle":"monthly"}`, "raw-one-cent")
	if response.Code != 409 || calls.Load() != 0 {
		t.Fatalf("raw positive intention created a physical checkout: status=%d calls=%d", response.Code, calls.Load())
	}
	var intents int
	if err = s.p.QueryRow(ctx, "SELECT count(*) FROM payment_checkout_intents WHERE tenant_id=$1 AND order_id=$2", s.tid, order.ID).Scan(&intents); err != nil || intents != 0 {
		t.Fatal("unsealed intention wrote a provider request", err)
	}
}

func TestApplicationCannotForgeDerivedBillingFactsOrRewriteOrderIntent(t *testing.T) {
	s := newBillingScenario(t)
	ctx := t.Context()
	quote := s.post(t, "/quote", `{"plan_code":"high","billing_cycle":"monthly"}`, "")
	if quote.Code != 200 {
		t.Fatalf("quote %d", quote.Code)
	}
	created := s.post(t, "/checkout", `{"quote":`+quote.Body.String()+`}`, "authorized-pending")
	if created.Code != 201 {
		t.Fatalf("checkout %d %s", created.Code, created.Body.String())
	}
	var body struct {
		Order store.Orders `json:"order"`
	}
	if json.Unmarshal(created.Body.Bytes(), &body) != nil {
		t.Fatal("missing order")
	}
	denied := func(statement string, args ...any) {
		t.Helper()
		tx, err := s.a.UserTx(ctx, s.tid, s.uid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err = tx.Exec(ctx, statement, args...); err == nil {
			t.Fatalf("app forged authoritative billing state with %s", statement)
		}
	}
	for _, table := range []string{"billing_order_authorizations", "invoices", "commissions", "order_items"} {
		denied("INSERT INTO " + table + " DEFAULT VALUES")
	}
	denied("UPDATE orders SET amount_cents=amount_cents+1 WHERE tenant_id=$1 AND id=$2", s.tid, body.Order.ID)
	denied("UPDATE orders SET status='paid' WHERE tenant_id=$1 AND id=$2", s.tid, body.Order.ID)
	tx, err := s.a.UserTx(ctx, s.tid, s.uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.New(tx).CancelPendingOrder(ctx, store.CancelPendingOrderParams{TenantID: s.tid, ID: body.Order.ID}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	denied("UPDATE orders SET status='pending' WHERE tenant_id=$1 AND id=$2", s.tid, body.Order.ID)
}

func TestSignedQuoteRejectsPersistedSnapshotPoisoning(t *testing.T) {
	for _, column := range []string{"plan_snapshot", "document_snapshot", "payload"} {
		t.Run(column, func(t *testing.T) {
			s := newBillingScenario(t)
			ctx := t.Context()
			response := s.post(t, "/quote", `{"plan_code":"high","billing_cycle":"monthly"}`, "")
			if response.Code != 200 {
				t.Fatalf("quote %d", response.Code)
			}
			var quote billing.Quote
			if err := json.Unmarshal(response.Body.Bytes(), &quote); err != nil {
				t.Fatal(err)
			}
			// Trusted fixture mutation emulates corruption after signing. The app role
			// itself cannot UPDATE the persisted quote row; checkout must still detect it.
			path, value := "{poisoned}", "true"
			if column == "payload" {
				path, value = "{final_amount_cents}", "0"
			}
			statement := "UPDATE billing_quotes SET " + column + "=jsonb_set(" + column + ",'" + path + "', '" + value + "'::jsonb,true) WHERE tenant_id=$1 AND digest=$2"
			if _, err := s.p.Exec(ctx, statement, s.tid, quote.Digest); err != nil {
				t.Fatal(err)
			}
			got := s.post(t, "/checkout", `{"quote":`+response.Body.String()+`}`, "poisoned-"+column)
			if got.Code != 409 {
				t.Fatalf("tampered persisted %s accepted: %d %s", column, got.Code, got.Body.String())
			}
			var count int
			if err := s.p.QueryRow(ctx, "SELECT count(*) FROM orders WHERE tenant_id=$1", s.tid).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid signed snapshot created an order: %d %v", count, err)
			}
		})
	}
}

func TestApplicationCannotMoveOrPrematurelyReleaseCouponReservation(t *testing.T) {
	s := newBillingScenario(t)
	ctx := t.Context()
	if _, err := s.p.Exec(ctx, "INSERT INTO coupons(code,kind,value) VALUES('boundary-coupon','percent',10)"); err != nil {
		t.Fatal(err)
	}
	quote := s.post(t, "/quote", `{"plan_code":"high","billing_cycle":"monthly","coupon_code":"boundary-coupon"}`, "")
	if quote.Code != 200 {
		t.Fatalf("quote: %s", quote.Body.String())
	}
	response := s.post(t, "/checkout", `{"quote":`+quote.Body.String()+`}`, "reserved-coupon")
	if response.Code != 201 {
		t.Fatalf("checkout: %s", response.Body.String())
	}
	var result struct {
		Order store.Orders `json:"order"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"UPDATE coupon_redemptions SET order_id=NULL WHERE tenant_id=$1", "DELETE FROM coupon_redemptions WHERE tenant_id=$1"} {
		tx, err := s.a.UserTx(ctx, s.tid, s.uid)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, statement, s.tid)
		_ = tx.Rollback(ctx)
		if err == nil {
			t.Fatalf("app changed active reservation: %s", statement)
		}
	}
	tx, err := s.a.UserTx(ctx, s.tid, s.uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.New(tx).CancelPendingOrder(ctx, store.CancelPendingOrderParams{TenantID: s.tid, ID: result.Order.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "DELETE FROM coupon_redemptions WHERE tenant_id=$1", s.tid); err != nil {
		t.Fatal("canceled reservation could not be released", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
