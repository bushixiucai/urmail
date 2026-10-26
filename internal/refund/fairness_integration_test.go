package refund_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/payment"
	refundService "github.com/bushixiucai/urmail/internal/refund"
	"github.com/bushixiucai/urmail/internal/store"
)

func secondRefundTenant(t *testing.T, first *refundFixture) *refundFixture {
	t.Helper()
	ctx := context.Background()
	f := &refundFixture{p: first.p, a: first.a, uid: first.uid}
	if err := f.p.QueryRow(ctx, `INSERT INTO tenants(brand_id,name,slug,status,owner_user_id) SELECT brand_id,'Other refund tenant','other-refund','active',$2 FROM tenants WHERE id=$1 RETURNING id`, first.tid, f.uid).Scan(&f.tid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(ctx, `INSERT INTO user_tenant_roles VALUES($1,$2,'owner')`, f.uid, f.tid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'other-refund-proof.test','active',now(),'other-fixture-proof')`, f.tid); err != nil {
		t.Fatal(err)
	}
	h := handlers.NewBillingHandler(store.New(f.a))
	h.Pool = f.p
	h.QuoteHMACKey = []byte("refund-fixture-key-with-at-least-thirty-two-bytes")
	f.r = gin.New()
	f.r.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, f.tid); c.Set(middleware.ContextUserID, f.uid) }, middleware.RequireTenantTx(f.a, zap.NewNop()))
	f.r.POST("/quote", h.Quote)
	f.r.POST("/checkout", h.Checkout)
	return f
}

func successfulRefundAdapter(t *testing.T, posts *atomic.Int32) *payment.Stripe {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Error("unexpected provider operation")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var amount int64
		if _, err := fmt.Sscan(r.Form.Get("amount"), &amount); err != nil {
			t.Error(err)
			return
		}
		n := posts.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("re_fair_%d", n), "status": "succeeded", "amount": amount, "currency": "cny", "payment_intent": r.Form.Get("payment_intent")})
	}))
	t.Cleanup(server.Close)
	adapter := payment.NewStripe("fixture", "fixture")
	adapter.Base = server.URL
	return adapter
}

func TestUnavailableRefundProviderDoesNotBlockAnotherTenant(t *testing.T) {
	first := newRefundFixture(t)
	second := secondRefundTenant(t, first)
	ctx := context.Background()
	badOrder, badReceipt := first.paid(t, "fair-bad")
	bad := first.approved(t, badOrder, badReceipt, "RF-fair-bad", "none", time.Time{})
	goodOrder, goodReceipt := second.paid(t, "fair-good")
	good := second.approved(t, goodOrder, goodReceipt, "RF-fair-good", "none", time.Time{})
	var posts, badLoads atomic.Int32
	adapter := successfulRefundAdapter(t, &posts)
	var recovered atomic.Bool
	svc := &refundService.Service{Pool: first.p, Enabled: true, ProviderFor: func(_ context.Context, receipt store.PlatformGetRefundPaymentRow) (payment.Provider, error) {
		if receipt.TenantID == first.tid && !recovered.Load() {
			badLoads.Add(1)
			return nil, errors.New("fixture merchant unavailable")
		}
		return adapter, nil
	}}
	if dispatched, err := svc.Step(ctx); !dispatched || err == nil {
		t.Fatalf("good tenant was blocked or bad provider was hidden: %v %v", dispatched, err)
	}
	q := store.New(first.p)
	badState, err := q.PlatformGetRefundForUpdate(ctx, bad.ID)
	if err != nil || badState.Status != "approved" || badState.DispatchAttempts != 1 || !badState.NextAttemptAt.Time.After(time.Now()) || badState.LastErrorCode.String != "provider_unavailable" {
		t.Fatalf("bad merchant did not enter durable backoff: %+v %v", badState, err)
	}
	goodState, err := q.PlatformGetRefundForUpdate(ctx, good.ID)
	if err != nil || goodState.Status != "completed" || posts.Load() != 1 {
		t.Fatalf("good tenant was not refunded: %+v %v posts=%d", goodState, err, posts.Load())
	}
	// A new worker instance respects durable retry dates, not just local memory.
	restarted := *svc
	if dispatched, err := restarted.Step(ctx); dispatched || err != nil || badLoads.Load() != 1 {
		t.Fatalf("restart ignored durable backoff: %v %v loads=%d", dispatched, err, badLoads.Load())
	}
	recovered.Store(true)
	if _, err := first.p.Exec(ctx, `UPDATE refunds SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, bad.ID); err != nil {
		t.Fatal(err)
	}
	if dispatched, err := restarted.Step(ctx); !dispatched || err != nil || posts.Load() != 2 {
		t.Fatalf("recovered merchant did not resume exactly once: %v %v posts=%d", dispatched, err, posts.Load())
	}
	if dispatched, err := restarted.Step(ctx); dispatched || err != nil || posts.Load() != 2 {
		t.Fatal("completed refund was dispatched again")
	}
}

func TestConcurrentRefundWorkersClaimOnePhysicalPayout(t *testing.T) {
	f := newRefundFixture(t)
	ctx := context.Background()
	order, receipt := f.paid(t, "fair-concurrent")
	f.approved(t, order, receipt, "RF-fair-concurrent", "none", time.Time{})
	var posts atomic.Int32
	adapter := successfulRefundAdapter(t, &posts)
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	svc := &refundService.Service{Pool: f.p, Enabled: true, ProviderFor: func(ctx context.Context, _ store.PlatformGetRefundPaymentRow) (payment.Provider, error) {
		ready <- struct{}{}
		select {
		case <-release:
			return adapter, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := svc.Step(ctx); errs <- err })
	}
	for range 2 {
		select {
		case <-ready:
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatal("workers did not reach the pre-claim barrier")
		}
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if posts.Load() != 1 {
		t.Fatalf("physical refund dispatched %d times", posts.Load())
	}
}

func TestRefundEntitlementFailureDoesNotBlockAnotherTenant(t *testing.T) {
	first := newRefundFixture(t)
	second := secondRefundTenant(t, first)
	ctx := context.Background()
	aOrder, aReceipt := first.paid(t, "entitlement-a")
	a := first.approved(t, aOrder, aReceipt, "RF-entitlement-a", "terminate_current", time.Now().Add(-time.Minute))
	bOrder, bReceipt := second.paid(t, "entitlement-b")
	b := second.approved(t, bOrder, bReceipt, "RF-entitlement-b", "terminate_current", time.Now().Add(-time.Minute))
	var posts atomic.Int32
	adapter := successfulRefundAdapter(t, &posts)
	svc := &refundService.Service{Pool: first.p, Enabled: true, ProviderFor: func(context.Context, store.PlatformGetRefundPaymentRow) (payment.Provider, error) {
		return adapter, nil
	}}
	for range 2 {
		if dispatched, err := svc.Step(ctx); !dispatched || err != nil {
			t.Fatalf("refund dispatch: %v %v", dispatched, err)
		}
	}
	if _, err := first.p.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION fail_one_entitlement_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='APPLY refund.entitlement' AND NEW.target_id='%d' THEN RAISE EXCEPTION 'fixture entitlement audit failure'; END IF; RETURN NEW; END$$; CREATE TRIGGER fail_one_entitlement_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION fail_one_entitlement_audit()`, a.ID)); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyEntitlements(ctx); err == nil {
		t.Fatal("expected one entitlement failure")
	}
	q := store.New(first.p)
	bad, err := q.PlatformGetRefundForUpdate(ctx, a.ID)
	if err != nil || bad.EntitlementAppliedAt.Valid || !bad.NextEntitlementAttemptAt.Time.After(time.Now()) {
		t.Fatalf("failed entitlement did not roll back and defer: %+v %v", bad, err)
	}
	good, err := q.PlatformGetRefundForUpdate(ctx, b.ID)
	if err != nil || !good.EntitlementAppliedAt.Valid {
		t.Fatalf("unrelated entitlement was blocked: %+v %v", good, err)
	}
}

func TestRefundSchedulingMetadataDoesNotBypassApproval(t *testing.T) {
	f := newRefundFixture(t)
	ctx := context.Background()
	order, receipt := f.paid(t, "stale-approval")
	row := f.approved(t, order, receipt, "RF-stale-approval", "none", time.Time{})
	if _, err := f.p.Exec(ctx, `DELETE FROM platform_admins WHERE user_id=$1`, f.uid); err != nil {
		t.Fatal(err)
	}
	q := store.New(f.p)
	// A stale approval must not prevent the scheduler from backing off.
	if err := q.PlatformFlagApprovedRefund(ctx, store.PlatformFlagApprovedRefundParams{TenantID: f.tid, ID: row.ID, LastErrorCode: pgtype.Text{String: "claim_rejected", Valid: true}}); err != nil {
		t.Fatalf("scheduling an invalid approval failed: %v", err)
	}
	if _, err := f.p.Exec(ctx, `UPDATE refunds SET amount_cents=amount_cents-1 WHERE id=$1`, row.ID); err == nil {
		t.Fatal("metadata exception allowed an unapproved amount change")
	}
	if _, err := f.p.Exec(ctx, `UPDATE refunds SET status='processing' WHERE id=$1`, row.ID); err == nil {
		t.Fatal("metadata exception authorized a physical payout claim")
	}
	tx, err := f.a.UserTx(ctx, f.tid, f.uid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = store.New(tx).PlatformFlagApprovedRefund(ctx, store.PlatformFlagApprovedRefundParams{TenantID: f.tid, ID: row.ID, LastErrorCode: pgtype.Text{String: "forged", Valid: true}}); err == nil {
		t.Fatal("tenant application role changed scheduling metadata")
	}
}
