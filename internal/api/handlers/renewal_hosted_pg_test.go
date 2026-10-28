package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/billingrenew"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/payment"
)

// This provider is an explicit stand-in. SQL, paid quote, HTTP and CSRF are real.
type hostedProviderFixture struct {
	*recurringSandboxAdapter
	mutex                          sync.Mutex
	starts, lookups, verifies      int
	request                        billingrenew.HostedAuthorizationRequest
	lost, wrongActor, wrongRequest bool
	beforeStart, beforeVerify      func() error
}

func (a *hostedProviderFixture) StartAuthorization(_ context.Context, _ payment.Merchant, r billingrenew.HostedAuthorizationRequest) (billingrenew.HostedAuthorizationSession, error) {
	a.mutex.Lock()
	a.starts++
	a.request = r
	hook := a.beforeStart
	lost := a.lost
	a.mutex.Unlock()
	if hook != nil {
		if e := hook(); e != nil {
			return billingrenew.HostedAuthorizationSession{}, e
		}
	}
	if lost {
		return billingrenew.HostedAuthorizationSession{}, billingrenew.ErrHostedAuthorizationUncertain
	}
	return a.session(), nil
}
func (a *hostedProviderFixture) session() billingrenew.HostedAuthorizationSession {
	return billingrenew.HostedAuthorizationSession{Reference: "cs_test_hostedfixture", URL: "https://checkout.stripe.com/c/pay/cs_test_hostedfixture", ExpiresAt: time.Now().Add(time.Hour)}
}
func (a *hostedProviderFixture) LookupAuthorization(context.Context, payment.Merchant, billingrenew.HostedAuthorizationRequest) (billingrenew.HostedAuthorizationSession, error) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	a.lookups++
	return a.session(), nil
}
func (a *hostedProviderFixture) VerifyAuthorization(_ context.Context, _ payment.Merchant, ref string) (billingrenew.VerifiedAuthorization, error) {
	a.mutex.Lock()
	a.verifies++
	r := a.request
	badActor, badRequest, hook := a.wrongActor, a.wrongRequest, a.beforeVerify
	a.mutex.Unlock()
	if ref != "cs_test_hostedfixture" {
		return billingrenew.VerifiedAuthorization{}, errors.New("arbitrary session refused")
	}
	if hook != nil {
		if e := hook(); e != nil {
			return billingrenew.VerifiedAuthorization{}, e
		}
	}
	out := billingrenew.VerifiedAuthorization{TenantID: r.TenantID, SubscriptionID: r.SubscriptionID, ActorID: r.ActorID, RequestID: r.RequestID, Reference: "pm_verified|cus_verified", Label: "Synthetic verified card", Currency: r.Currency, ExpiresAt: time.Now().AddDate(1, 0, 0)}
	if badActor {
		out.ActorID++
	}
	if badRequest {
		out.RequestID = "unrelated-server-intent"
	}
	return out, nil
}
func hostedFixture(t *testing.T) (*recurringFixture, *hostedProviderFixture, string) {
	t.Helper()
	f := newRecurringFixture(t)
	adapter := &hostedProviderFixture{recurringSandboxAdapter: f.a}
	f.d.Adapters["isolated-sandbox-v1"] = adapter
	RegisterHostedRenewalRoutes(f.router.Group("/api/v1"), f.service)
	overview, e := f.service.Overview(t.Context(), f.s.tid)
	if e != nil || overview.Preview == nil || !overview.Preview.Eligible {
		t.Fatal(overview, e)
	}
	return f, adapter, overview.Preview.QuoteDigest
}
func hostedStart(t *testing.T, f *recurringFixture, key, digest string) (billingrenew.HostedSetup, error) {
	t.Helper()
	return f.service.StartHostedSetup(t.Context(), f.s.tid, f.s.uid, f.merchant, key, digest, true)
}

func TestHostedRecurringDurableIntentLostACKAndExplicitEnrollment_PG(t *testing.T) {
	f, a, digest := hostedFixture(t)
	a.lost = true
	a.beforeStart = func() error {
		var state string
		if e := f.s.p.QueryRow(t.Context(), "SELECT state FROM recurring_setup_intents WHERE tenant_id=$1 AND actor_id=$2", f.s.tid, f.s.uid).Scan(&state); e != nil {
			return e
		}
		if state != "creating" {
			return fmt.Errorf("provider POST before durable intent: %s", state)
		}
		tx, e := f.s.p.Begin(t.Context())
		if e != nil {
			return e
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		var id int64
		return tx.QueryRow(t.Context(), "SELECT id FROM recurring_setup_intents WHERE tenant_id=$1 AND actor_id=$2 FOR UPDATE NOWAIT", f.s.tid, f.s.uid).Scan(&id)
	}
	setup, e := hostedStart(t, f, "hosted-lost-create-key", digest)
	if e != nil || setup.State != "uncertain" || a.starts != 1 {
		t.Fatal("lost provider ACK", setup, e)
	}
	replay, e := hostedStart(t, f, "hosted-lost-create-key", digest)
	if e != nil || replay.ID != setup.ID || a.starts != 1 {
		t.Fatal("blind POST retry", replay, e)
	}
	recovered, e := f.service.ReconcileHostedSetup(t.Context(), f.s.tid, f.s.uid, setup.ID, setup.Version)
	if e != nil || recovered.State != "ready" || a.starts != 1 || a.lookups != 1 {
		t.Fatal("read-only hosted recovery", recovered, e)
	}
	a.wrongRequest = true
	if _, e = f.service.CompleteHostedSetup(t.Context(), f.s.tid, f.s.uid, recovered.ID, recovered.Version); e == nil {
		t.Fatal("cross-intent provider metadata accepted")
	}
	a.wrongRequest = false
	a.wrongActor = true
	if _, e = f.service.CompleteHostedSetup(t.Context(), f.s.tid, f.s.uid, recovered.ID, recovered.Version); e == nil {
		t.Fatal("cross-actor provider metadata accepted")
	}
	a.wrongActor = false
	complete, e := f.service.CompleteHostedSetup(t.Context(), f.s.tid, f.s.uid, recovered.ID, recovered.Version)
	if e != nil || complete.State != "verified" || complete.BindingID == nil {
		t.Fatal("verified binding", complete, e)
	}
	var enrollments int
	if e = f.s.p.QueryRow(t.Context(), "SELECT count(*) FROM subscription_renewal_enrollments WHERE tenant_id=$1", f.s.tid).Scan(&enrollments); e != nil || enrollments != 0 {
		t.Fatal("hosted setup silently enrolled", enrollments, e)
	}
	if _, e = f.service.Enroll(t.Context(), f.s.tid, f.s.uid, *complete.BindingID, billingrenew.ConsentVersion, digest, true); e != nil {
		t.Fatal("separate explicit consent failed", e)
	}
	again, e := f.service.CompleteHostedSetup(t.Context(), f.s.tid, f.s.uid, complete.ID, recovered.Version)
	if e != nil || again.BindingID == nil || *again.BindingID != *complete.BindingID {
		t.Fatal("completed proof replay changed binding", again, e)
	}
}

func TestHostedRecurringRoleAndMerchantRaceAndCrossTenantRefusal_PG(t *testing.T) {
	f, a, digest := hostedFixture(t)
	setup, e := hostedStart(t, f, "hosted-role-race-key", digest)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.GetHostedSetup(t.Context(), f.s.tid+1, f.s.uid, setup.ID); e == nil {
		t.Fatal("cross tenant setup disclosed")
	}
	a.beforeVerify = func() error {
		_, err := f.s.p.Exec(t.Context(), "DELETE FROM user_tenant_roles WHERE tenant_id=$1 AND user_id=$2", f.s.tid, f.s.uid)
		return err
	}
	if _, e = f.service.CompleteHostedSetup(t.Context(), f.s.tid, f.s.uid, setup.ID, setup.Version); e == nil {
		t.Fatal("revoked role completed after provider lookup")
	}
	var binding int
	if e = f.s.p.QueryRow(t.Context(), "SELECT count(*) FROM recurring_setup_intents WHERE tenant_id=$1 AND binding_id IS NOT NULL", f.s.tid).Scan(&binding); e != nil || binding != 0 {
		t.Fatal("revoked actor wrote binding", binding, e)
	}
}

func TestHostedRecurringHTTPRequiresCSRFAndRejectsProviderReferences_PG(t *testing.T) {
	f, _, _ := hostedFixture(t)
	rec := f.request(t, "POST", "/authorizations", map[string]any{"merchant_id": f.merchant, "quote_digest": "unused", "confirmed": true}, false)
	if rec.Code != 403 {
		t.Fatal("hosted setup missing CSRF accepted", rec.Code, rec.Body.String())
	}
	rec = f.request(t, "POST", "/authorizations/1/complete", map[string]any{"version": 1, "provider_reference": "cs_attacker"}, true)
	if rec.Code != 400 {
		t.Fatal("caller provider reference accepted", rec.Code, rec.Body.String())
	}
	rec = f.request(t, "GET", "/authorizations?request_key=missing-key", nil, false)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var out map[string]any
	if e := json.Unmarshal(rec.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
}

func TestHostedRecurringMerchantChangeRestoreAndLateCancellation_PG(t *testing.T) {
	t.Run("merchant_race", func(t *testing.T) {
		f, a, digest := hostedFixture(t)
		setup, e := hostedStart(t, f, "merchant-race-key", digest)
		if e != nil {
			t.Fatal(e)
		}
		a.beforeVerify = func() error {
			_, err := f.s.p.Exec(t.Context(), "UPDATE payment_merchants SET updated_at=now()+interval '1 second' WHERE id=$1", f.merchant)
			return err
		}
		if _, e = f.service.CompleteHostedSetup(t.Context(), f.s.tid, f.s.uid, setup.ID, setup.Version); e == nil {
			t.Fatal("changed merchant completed old authorization")
		}
	})
	t.Run("restore_proof", func(t *testing.T) {
		f, a, digest := hostedFixture(t)
		setup, e := hostedStart(t, f, "restore-hosted-key", digest)
		if e != nil {
			t.Fatal(e)
		}
		if e = db.HoldRestoredSideEffects(t.Context(), f.s.p.Config().ConnString(), "hosted-150-owned-restore"); e != nil {
			t.Fatal(e)
		}
		held, e := f.service.GetHostedSetup(t.Context(), f.s.tid, f.s.uid, setup.ID)
		if e != nil || held.State != "held" || held.URL != "" || held.Version <= setup.Version {
			t.Fatal("old restored hosted proof survived", held, e)
		}
		if _, e = f.service.CompleteHostedSetup(t.Context(), f.s.tid, f.s.uid, setup.ID, setup.Version); e == nil || a.verifies != 0 {
			t.Fatal("restored proof contacted provider verifier", e)
		}
		var count int
		if e = f.s.a.QueryRow(t.Context(), "SELECT count(*) FROM recurring_setup_intents").Scan(&count); e == nil {
			t.Fatal("unprivileged app could read provider setup facts")
		}
	})
	t.Run("cancel_during_provider_create", func(t *testing.T) {
		f, a, digest := hostedFixture(t)
		a.beforeStart = func() error {
			var id, version int64
			if e := f.s.p.QueryRow(t.Context(), "SELECT id,version FROM recurring_setup_intents WHERE tenant_id=$1", f.s.tid).Scan(&id, &version); e != nil {
				return e
			}
			_, e := f.service.CancelHostedSetup(t.Context(), f.s.tid, f.s.uid, id, version)
			return e
		}
		setup, e := hostedStart(t, f, "cancel-during-create-key", digest)
		if e != nil || setup.State != "canceled" || setup.URL != "" {
			t.Fatal("late create reactivated canceled proof", setup, e)
		}
		var reference string
		if e = f.s.p.QueryRow(t.Context(), "SELECT provider_reference FROM recurring_setup_intents WHERE tenant_id=$1 AND id=$2", f.s.tid, setup.ID).Scan(&reference); e != nil || reference != "cs_test_hostedfixture" {
			t.Fatal("actual provider fact lost after cancellation", reference, e)
		}
	})
}

func TestHostedRecurringActualHTTPStartRecoverCompleteAndQuoteConflict_PG(t *testing.T) {
	f, a, digest := hostedFixture(t)
	data, _ := json.Marshal(map[string]any{"merchant_id": f.merchant, "quote_digest": digest, "confirmed": true})
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/billing/auto-renewal/authorizations", strings.NewReader(string(data)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "http-hosted-setup-key")
	req.Header.Set(middleware.CSRFHeader, f.csrf)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal("actual hosted start HTTP", rec.Code, rec.Body.String())
	}
	var result struct {
		Authorization billingrenew.HostedSetup `json:"authorization"`
	}
	if e := json.Unmarshal(rec.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if result.Authorization.State != "ready" || result.Authorization.URL == "" || a.starts != 1 {
		t.Fatal("hosted response", result)
	}
	get := f.request(t, "GET", "/authorizations?request_key=http-hosted-setup-key", nil, false)
	if get.Code != 200 || !strings.Contains(get.Body.String(), fmt.Sprint(result.Authorization.ID)) {
		t.Fatal("recover lost response", get.Code, get.Body.String())
	}
	done := f.request(t, "POST", fmt.Sprintf("/authorizations/%d/complete", result.Authorization.ID), map[string]any{"version": result.Authorization.Version}, true)
	if done.Code != 200 {
		t.Fatal("actual complete HTTP", done.Code, done.Body.String())
	}
	if strings.Contains(done.Body.String(), "provider_reference") || strings.Contains(done.Body.String(), "pm_verified") {
		t.Fatal("provider reference leaked")
	}
	if _, e := hostedStart(t, f, "http-hosted-setup-key", strings.Repeat("a", 64)); e == nil {
		t.Fatal("same key changed quote accepted")
	}
}
