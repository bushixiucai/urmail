package handlers

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/billingrenew"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
)

// recurringSandboxAdapter is an explicit provider stand-in, not a merchant
// approval or a real saved-instrument implementation.
type recurringSandboxAdapter struct {
	mu                   sync.Mutex
	tenant, subscription int64
	charges, queries     int
	result               billingrenew.ChargeOutcome
	err                  error
	block, started       chan struct{}
}

func (*recurringSandboxAdapter) Name() string { return "stripe" }
func (a *recurringSandboxAdapter) VerifyMerchant(_ context.Context, m payment.Merchant) (billingrenew.MerchantEvidence, error) {
	if m.Environment != "sandbox" {
		return billingrenew.MerchantEvidence{}, errors.New("fixture accepts sandbox only")
	}
	return billingrenew.MerchantEvidence{Reference: "isolated sandbox adapter evidence", ExpiresAt: time.Now().Add(24 * time.Hour)}, nil
}
func (a *recurringSandboxAdapter) VerifyAuthorization(_ context.Context, _ payment.Merchant, ref string) (billingrenew.VerifiedAuthorization, error) {
	if ref != "provider-verified-fixture-reference" {
		return billingrenew.VerifiedAuthorization{}, errors.New("not owned")
	}
	return billingrenew.VerifiedAuthorization{TenantID: a.tenant, SubscriptionID: a.subscription, Reference: ref, Label: "Isolated verified sandbox authorization", Currency: "CNY", ExpiresAt: time.Now().AddDate(1, 0, 0)}, nil
}
func (a *recurringSandboxAdapter) Charge(ctx context.Context, req billingrenew.ChargeRequest) (billingrenew.ChargeOutcome, error) {
	a.mu.Lock()
	a.charges++
	a.mu.Unlock()
	if a.started != nil {
		close(a.started)
	}
	if a.block != nil {
		select {
		case <-ctx.Done():
			return billingrenew.ChargeOutcome{}, ctx.Err()
		case <-a.block:
		}
	}
	return a.answer(req), a.err
}
func (a *recurringSandboxAdapter) Query(_ context.Context, req billingrenew.ChargeRequest) (billingrenew.ChargeOutcome, error) {
	a.mu.Lock()
	a.queries++
	a.mu.Unlock()
	return a.answer(req), a.err
}
func (a *recurringSandboxAdapter) answer(req billingrenew.ChargeRequest) billingrenew.ChargeOutcome {
	out := a.result
	if out.Status == billingrenew.ChargeSucceeded {
		out.AmountCents = req.AmountCents
		out.Currency = req.Currency
		if out.ProviderTxnID == "" {
			out.ProviderTxnID = fmt.Sprintf("sandbox-renewal-%d", req.AttemptID)
		}
	}
	return out
}

type recurringFixture struct {
	s                               *billingScenario
	d                               *billingrenew.Dispatcher
	service                         billingrenew.EnrollmentService
	a                               *recurringSandboxAdapter
	binding, merchant, subscription int64
	router                          *gin.Engine
	csrf                            string
}

func newRecurringFixture(t *testing.T) *recurringFixture {
	t.Helper()
	s := newBillingScenario(t)
	s.pay(t, "high", "recurring-initial")
	f := &recurringFixture{s: s, csrf: "isolated-recurring-csrf"}
	if err := s.p.QueryRow(t.Context(), "SELECT id FROM subscriptions WHERE tenant_id=$1 AND status='active'", s.tid).Scan(&f.subscription); err != nil {
		t.Fatal(err)
	}
	// Move this owned test term to a due cycle without changing its frozen plan,
	// price or calendar. Never adjust a real subscription for a test.
	end := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	for _, sql := range []string{"UPDATE subscriptions SET period_start=$2::timestamptz-interval '1 month',period_end=$2 WHERE tenant_id=$1", "UPDATE subscription_terms SET period_start=$2::timestamptz-interval '1 month',billing_period_start=$2::timestamptz-interval '1 month',period_end=$2 WHERE tenant_id=$1"} {
		if _, err := s.p.Exec(t.Context(), sql, s.tid, end); err != nil {
			t.Fatal(err)
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	config, err := box.EncryptString(`{"secret_key":"sk_test_isolated_fixture","webhook_secret":"whsec_fixture"}`)
	if err != nil {
		t.Fatal(err)
	}
	q := store.New(s.p)
	m, err := q.PlatformCreatePaymentMerchant(t.Context(), store.PlatformCreatePaymentMerchantParams{Provider: "stripe", Environment: "sandbox", AccountID: "acct_recurringfixture", Name: "Isolated sandbox recurring fixture", Enabled: true, Currencies: []string{"CNY"}, Entrypoint: "hosted", EncryptedConfig: config, CheckoutLimits: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	f.merchant = m.ID
	f.a = &recurringSandboxAdapter{tenant: s.tid, subscription: f.subscription, result: billingrenew.ChargeOutcome{Status: billingrenew.ChargeUncertain}}
	f.d = &billingrenew.Dispatcher{Pool: s.p, Registry: &payment.Registry{Store: q, Secrets: box, BaseURL: "https://billing.example.test"}, Secrets: box, Adapters: map[string]billingrenew.RecurringAdapter{"isolated-sandbox-v1": f.a}}
	f.service = billingrenew.EnrollmentService{Dispatcher: f.d, QuoteKey: []byte("isolated-recurring-preview-hmac-key-32-bytes")}
	if err = f.d.ApproveMerchant(t.Context(), m.ID, "isolated-sandbox-v1"); err != nil {
		t.Fatal(err)
	}
	f.binding, err = f.d.VerifyBinding(t.Context(), s.tid, f.subscription, m.ID, "provider-verified-fixture-reference")
	if err != nil {
		t.Fatal(err)
	}
	f.router = gin.New()
	f.router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextTenantID, s.tid)
		c.Set(middleware.ContextUserID, s.uid)
		c.Set(middleware.ContextSession, store.Sessions{CsrfToken: f.csrf})
	}, middleware.RequireTenantTx(s.a, zap.NewNop()), middleware.RequireCSRF())
	RegisterRenewalRoutes(f.router.Group("/api/v1"), f.service)
	return f
}
func (f *recurringFixture) request(t *testing.T, method, path string, body any, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	var text string
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		text = string(raw)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, "/api/v1/billing/auto-renewal"+path, strings.NewReader(text))
	req.Header.Set("Content-Type", "application/json")
	if csrf {
		req.Header.Set(middleware.CSRFHeader, f.csrf)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}
func (f *recurringFixture) enroll(t *testing.T) {
	t.Helper()
	out, err := f.service.Overview(t.Context(), f.s.tid)
	if err != nil {
		t.Fatal(err)
	}
	if out.Preview == nil || !out.Preview.Eligible {
		t.Fatalf("preview %+v", out)
	}
	rec := f.request(t, "POST", "/enroll", map[string]any{"binding_id": f.binding, "consent_version": billingrenew.ConsentVersion, "quote_digest": out.Preview.QuoteDigest, "confirmed": true}, true)
	if rec.Code != 200 {
		t.Fatalf("enroll %d %s", rec.Code, rec.Body.String())
	}
}
func (f *recurringFixture) worker() billingrenew.Worker {
	return billingrenew.Worker{Store: store.New(f.s.p), Chargers: f.d, Coordinator: billingrenew.PoolCoordinator{Pool: f.s.p}, Settlements: billingrenew.PoolSettlementStore{Pool: f.s.p}}
}

func TestRecurringExplicitConsentAndCapabilityPG(t *testing.T) {
	f := newRecurringFixture(t)
	q := store.New(f.s.p)
	if err := q.PlatformEnqueueDueRenewals(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := q.PlatformListTenantRenewalAttempts(t.Context(), f.s.tid)
	if err != nil || len(rows) != 0 {
		t.Fatalf("paid auto-enrolled: %v %v", rows, err)
	}
	out, err := f.service.Overview(t.Context(), f.s.tid)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"binding_id": f.binding, "consent_version": billingrenew.ConsentVersion, "quote_digest": out.Preview.QuoteDigest, "confirmed": true}
	if r := f.request(t, "POST", "/enroll", payload, false); r.Code != 403 {
		t.Fatalf("missing csrf accepted: %d", r.Code)
	}
	payload["confirmed"] = false
	if r := f.request(t, "POST", "/enroll", payload, true); r.Code != 409 {
		t.Fatal("implicit consent accepted")
	}
	payload["confirmed"] = true
	payload["binding_id"] = f.binding + 100
	if r := f.request(t, "POST", "/enroll", payload, true); r.Code != 409 {
		t.Fatal("arbitrary binding accepted")
	}
	if _, err = f.d.VerifyBinding(t.Context(), f.s.tid+1, f.subscription, f.merchant, "provider-verified-fixture-reference"); err == nil {
		t.Fatal("provider authorization crossed tenant")
	}
	f.enroll(t)
	if err = q.PlatformEnqueueDueRenewals(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err = q.PlatformListTenantRenewalAttempts(t.Context(), f.s.tid)
	if err != nil || len(rows) != 1 {
		t.Fatalf("queue=%d %v", len(rows), err)
	}
	if r := f.request(t, "POST", "/cancel", map[string]any{}, true); r.Code != 200 {
		t.Fatalf("cancel=%d %s", r.Code, r.Body.String())
	}
	rows, _ = q.PlatformListTenantRenewalAttempts(t.Context(), f.s.tid)
	if rows[0].State != "canceled" {
		t.Fatal("unstarted charge was not canceled")
	}
	f.d.Adapters = nil
	out, err = f.service.Overview(t.Context(), f.s.tid)
	if err != nil || out.Preview.Eligible || out.Capabilities[0].Status != "unsupported" {
		t.Fatalf("missing adapter is eligible %+v %v", out, err)
	}
	if err = f.d.ApproveMerchant(t.Context(), f.merchant, "missing"); err == nil {
		t.Fatal("approved an unimplemented adapter")
	}
}

func TestRecurringUnknownReconcileAndManualCycleFencingPG(t *testing.T) {
	f := newRecurringFixture(t)
	f.enroll(t)
	q := store.New(f.s.p)
	f.a.err = errors.New("response lost after provider accepted request")
	if err := f.worker().RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := q.PlatformListTenantRenewalAttempts(t.Context(), f.s.tid)
	if err != nil || len(rows) != 1 || rows[0].State != "uncertain" || !rows[0].SideEffectIntentAt.Valid {
		t.Fatalf("lost response not held: %+v %v", rows, err)
	}
	if _, err = f.s.p.Exec(t.Context(), "UPDATE subscription_renewal_attempts SET next_attempt_at=now(),side_effect_intent_at=now()-interval '1 year' WHERE tenant_id=$1", f.s.tid); err != nil {
		t.Fatal(err)
	}
	if err = f.worker().RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.a.charges != 1 || f.a.queries != 1 {
		t.Fatalf("unsafe retry charge=%d query=%d", f.a.charges, f.a.queries)
	}
	_, flight, err := f.service.Cancel(t.Context(), f.s.tid)
	if err != nil || !flight {
		t.Fatalf("cancel lost inflight %v %v", flight, err)
	}
	// A verified active checkout advanced the same cycle while its debit was
	// unknown. Confirmed money remains recorded, but cannot extend it again.
	var before time.Time
	f.s.pay(t, "high", "manual-during-uncertain-renewal")
	if err = f.s.p.QueryRow(t.Context(), "SELECT period_end FROM subscriptions WHERE tenant_id=$1 AND status='active'", f.s.tid).Scan(&before); err != nil {
		t.Fatal(err)
	}
	f.a.err = nil
	f.a.result = billingrenew.ChargeOutcome{Status: billingrenew.ChargeSucceeded}
	if _, err = f.s.p.Exec(t.Context(), "UPDATE subscription_renewal_attempts SET next_attempt_at=now() WHERE tenant_id=$1", f.s.tid); err != nil {
		t.Fatal(err)
	}
	if err = f.worker().RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	var payments int
	if err = f.s.p.QueryRow(t.Context(), "SELECT period_end FROM subscriptions WHERE tenant_id=$1 AND status='active'", f.s.tid).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Fatal("manual payment plus uncertain charge extended twice")
	}
	if err = f.s.p.QueryRow(t.Context(), "SELECT count(*) FROM payments WHERE tenant_id=$1 AND account_scope='stripe:sandbox:acct_recurringfixture'", f.s.tid).Scan(&payments); err != nil || payments != 1 {
		t.Fatalf("financial receipt missing %d %v", payments, err)
	}
	var reviews int
	if err = f.s.p.QueryRow(t.Context(), "SELECT count(*) FROM payment_manual_reviews WHERE tenant_id=$1 AND reason='RENEWAL_CYCLE_ALREADY_FUNDED' AND status='open'", f.s.tid).Scan(&reviews); err != nil || reviews != 1 {
		t.Fatalf("unapplied money was not reviewable: %d %v", reviews, err)
	}

}

func TestRecurringSettlementWritesExistingEntitlementLedgerPG(t *testing.T) {
	f := newRecurringFixture(t)
	f.enroll(t)
	f.a.result = billingrenew.ChargeOutcome{Status: billingrenew.ChargeSucceeded}
	if err := f.worker().RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var terms, invoices int
	if err := f.s.p.QueryRow(t.Context(), "SELECT count(*) FROM subscription_terms WHERE tenant_id=$1", f.s.tid).Scan(&terms); err != nil {
		t.Fatal(err)
	}
	if err := f.s.p.QueryRow(t.Context(), "SELECT count(*) FROM invoices WHERE tenant_id=$1 AND source_payment_id IS NOT NULL", f.s.tid).Scan(&invoices); err != nil {
		t.Fatal(err)
	}
	if terms != 2 || invoices != 2 {
		t.Fatalf("renewal skipped canonical ledger terms=%d invoices=%d", terms, invoices)
	}
	rows, err := store.New(f.s.p).PlatformListTenantRenewalAttempts(t.Context(), f.s.tid)
	if err != nil {
		t.Fatal(err)
	}
	row := rows[0]
	if _, err = billingrenew.PlatformSettleRenewalAttempt(t.Context(), f.s.p, billingrenew.RenewalSettlementParams{TenantID: f.s.tid, AttemptID: row.ID, Provider: "stripe", AccountScope: row.MerchantScope, ProviderTxnID: fmt.Sprintf("sandbox-renewal-%d", row.ID), AmountCents: row.AmountCents.Int64 + 1, Currency: row.Currency.String, DedupeKey: row.DedupeKey}); !errors.Is(err, billingrenew.ErrRenewalSettlementConflict) {
		t.Fatal("changed amount accepted on replay")
	}
}

func TestRecurringClaimLeaseAndCancelFencePG(t *testing.T) {
	f := newRecurringFixture(t)
	f.enroll(t)
	q := store.New(f.s.p)
	if err := q.PlatformEnqueueDueRenewals(t.Context()); err != nil {
		t.Fatal(err)
	}
	first, err := q.PlatformClaimRenewalAttempt(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.p.Exec(t.Context(), "UPDATE subscription_renewal_attempts SET lease_until=now()-interval '1 minute' WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	if err = q.PlatformRecoverRenewalLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	second, err := q.PlatformClaimRenewalAttempt(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if second.ClaimToken <= first.ClaimToken {
		t.Fatal("lease token did not advance")
	}
	coordinator := billingrenew.PoolCoordinator{Pool: f.s.p}
	if _, err = coordinator.BeginCharge(t.Context(), first, "stale-key"); err == nil {
		t.Fatal("stale claimant created intent")
	}
	if _, _, err = f.service.Cancel(t.Context(), f.s.tid); err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.BeginCharge(t.Context(), second, "revoked-key"); err == nil {
		t.Fatal("revoked enrollment created intent")
	}
	rows, err := q.PlatformListTenantRenewalAttempts(t.Context(), f.s.tid)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].SideEffectIntentAt.Valid || rows[0].State != "canceled" {
		t.Fatalf("revoked claim mutated %+v", rows[0])
	}
}

func TestRecurringKnownDeclineBackoffPG(t *testing.T) {
	f := newRecurringFixture(t)
	f.enroll(t)
	f.a.result = billingrenew.ChargeOutcome{Status: billingrenew.ChargeFailed}
	q := store.New(f.s.p)
	var previousKey string
	for round := 1; round <= 5; round++ {
		if round > 1 {
			if _, err := f.s.p.Exec(t.Context(), "UPDATE subscription_renewal_attempts SET next_attempt_at=now() WHERE tenant_id=$1", f.s.tid); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.worker().RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		rows, err := q.PlatformListTenantRenewalAttempts(t.Context(), f.s.tid)
		if err != nil {
			t.Fatal(err)
		}
		row := rows[0]
		if row.ChargeCount != int32(round) {
			t.Fatalf("charge generation=%d round=%d", row.ChargeCount, round)
		}
		var history []struct {
			Key string `json:"key"`
		}
		if json.Unmarshal(row.ChargeHistory, &history) != nil || len(history) != round {
			t.Fatal("decline intent history lost")
		}
		if history[round-1].Key == previousKey {
			t.Fatal("a definite decline reused an old charge key")
		}
		previousKey = history[round-1].Key
		if round < 5 && row.State != "pending" {
			t.Fatalf("bounded decline should retry: %s", row.State)
		}
		if round == 5 && row.State != "failed" {
			t.Fatalf("fifth decline should stop: %s", row.State)
		}
	}
	if f.a.charges != 5 || f.a.queries != 0 {
		t.Fatalf("charge/query %d/%d", f.a.charges, f.a.queries)
	}
}
