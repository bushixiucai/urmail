package billingrenew

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

type stripeTestStore struct{ row store.PaymentMerchants }

func (s *stripeTestStore) PlatformGetPaymentMerchant(_ context.Context, id int64) (store.PaymentMerchants, error) {
	if id != s.row.ID {
		return store.PaymentMerchants{}, errors.New("missing merchant")
	}
	return s.row, nil
}
func (s *stripeTestStore) PlatformListPaymentMerchants(context.Context) ([]store.PlatformListPaymentMerchantsRow, error) {
	return nil, nil
}
func (s *stripeTestStore) PlatformFindPaymentMerchantByScope(_ context.Context, p store.PlatformFindPaymentMerchantByScopeParams) (store.PaymentMerchants, error) {
	if p.Provider != "stripe" || p.Provider_2 != "stripe:"+s.row.Environment+":"+s.row.AccountID {
		return store.PaymentMerchants{}, errors.New("scope mismatch")
	}
	return s.row, nil
}

type stripeTestBox struct{}

func (stripeTestBox) DecryptString(string) (string, error) {
	return `{"secret_key":"sk_test_controlled","webhook_secret":"whsec_controlled"}`, nil
}

type stripeTestTransport struct {
	target *url.URL
	next   http.RoundTripper
}

func (s stripeTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "api.stripe.com" {
		return nil, errors.New("nonofficial target")
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = s.target.Scheme
	u.Host = s.target.Host
	clone.URL = &u
	return s.next.RoundTrip(clone)
}

type stripeContract struct {
	t          *testing.T
	mu         sync.Mutex
	calls      []string
	posts      int
	mode       string
	request    HostedAuthorizationRequest
	charge     ChargeRequest
	reference  stripeSealedReference
	key        string
	account    string
	live       bool
	capability bool
}

func newStripeContract(t *testing.T) (*StripeRecurring, *stripeTestStore, *stripeContract) {
	t.Helper()
	c := &stripeContract{t: t, account: "acct_controlled", capability: true, request: HostedAuthorizationRequest{TenantID: 11, SubscriptionID: 22, ActorID: 33, RequestID: "01234567-89ab-cdef-0123-456789abcdef", Currency: "USD", SuccessURL: "https://panel.example.test/billing?setup=success", CancelURL: "https://panel.example.test/billing?setup=cancel"}}
	c.reference = stripeSealedReference{Version: 1, Account: c.account, Environment: "sandbox", Session: "cs_test_controlled", SetupIntent: "seti_controlled", Customer: "cus_controlled", PaymentMethod: "pm_controlled", TenantID: 11, SubscriptionID: 22, ActorID: 33, RequestID: c.request.RequestID, Currency: "USD", Expires: time.Now().Add(24 * time.Hour).Unix()}
	raw, _ := json.Marshal(c.reference)
	c.charge = ChargeRequest{TenantID: 11, SubscriptionID: 22, AttemptID: 44, AmountCents: 1200, Currency: "USD", PeriodEnd: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), IdempotencyKey: "renewal:22:44:charge-1", MerchantScope: "stripe:sandbox:" + c.account, AuthorizationReference: string(raw)}
	server := httptest.NewServer(c)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	st := &stripeTestStore{row: store.PaymentMerchants{ID: 1, Provider: "stripe", Environment: "sandbox", AccountID: c.account, Name: "Controlled", Enabled: true, Currencies: []string{"USD"}, Entrypoint: "hosted", CheckoutLimits: []byte(`{"USD":{"min_cents":50,"max_cents":100000,"settlement_currency":"USD","limits_source":"official"}}`), EncryptedConfig: "enc:v1:controlled"}}
	registry := &payment.Registry{Store: st, Secrets: stripeTestBox{}, BaseURL: "https://panel.example.test", Client: &http.Client{Transport: stripeTestTransport{target: target, next: http.DefaultTransport}}}
	return NewStripeRecurring(registry), st, c
}
func (c *stripeContract) setup(status string) map[string]any {
	return map[string]any{"id": "cs_test_controlled", "object": "checkout.session", "mode": "setup", "status": status, "customer": "cus_controlled", "setup_intent": "seti_controlled", "currency": "usd", "livemode": c.live, "client_reference_id": c.request.RequestID, "metadata": setupMetadata(c.request), "expires_at": time.Now().Add(time.Hour).Unix(), "url": "https://checkout.stripe.com/c/pay/cs_test_controlled#opaque"}
}
func (c *stripeContract) intent(id string) map[string]any {
	metadata := chargeMetadata(c.charge)
	status := "succeeded"
	received := c.charge.AmountCents
	if c.mode == "processing" {
		status = "processing"
		received = 0
	}
	if c.mode == "requires_action" {
		status = "requires_action"
		received = 0
	}
	out := map[string]any{"id": id, "object": "payment_intent", "status": status, "amount": c.charge.AmountCents, "amount_received": received, "currency": "usd", "customer": "cus_controlled", "payment_method": "pm_controlled", "livemode": c.live, "metadata": metadata}
	if c.mode == "decline" {
		out["status"] = "requires_payment_method"
		out["amount_received"] = 0
		out["last_payment_error"] = map[string]string{"type": "card_error", "code": "card_declined"}
	}
	if c.mode == "wrong_amount" {
		out["amount_received"] = 1
	}
	if c.mode == "wrong_tenant" {
		metadata["tenant_id"] = "999"
	}
	return out
}
func (c *stripeContract) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, r.Method+" "+r.URL.Path)
	user, password, ok := r.BasicAuth()
	if !ok || user != "sk_test_controlled" || password != "" {
		c.t.Error("credential header mismatch")
	}
	if r.Header.Get("Stripe-Version") != "2024-06-20" {
		c.t.Error("API version not pinned")
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/v1/balance" {
		switch c.mode {
		case "redirect":
			w.Header().Set("Location", "https://attacker.example.test/credential-sink")
			w.WriteHeader(http.StatusFound)
			_, _ = w.Write([]byte(`{}`))
			return
		case "oversized":
			_, _ = w.Write([]byte(strings.Repeat(" ", 1<<20) + `{}`))
			return
		case "malformed":
			_, _ = w.Write([]byte(`{"object":`))
			return
		}
	}
	var value any
	switch {
	case r.URL.Path == "/v1/account":
		capability := "inactive"
		if c.capability {
			capability = "active"
		}
		value = map[string]any{"id": c.account, "charges_enabled": c.capability, "capabilities": map[string]string{"card_payments": capability}}
	case r.URL.Path == "/v1/balance":
		value = map[string]any{"object": "balance", "livemode": c.live}
	case r.URL.Path == "/v1/customers" && r.Method == http.MethodPost:
		c.posts++
		_ = r.ParseForm()
		if r.Header.Get("Idempotency-Key") != setupKey(c.charge.MerchantScope, c.request.RequestID, "customer") {
			c.t.Error("unstable customer key")
		}
		value = map[string]any{"id": "cus_controlled", "object": "customer", "livemode": c.live, "metadata": setupMetadata(c.request)}
	case r.URL.Path == "/v1/checkout/sessions" && r.Method == http.MethodPost:
		c.posts++
		_ = r.ParseForm()
		if r.Form.Get("mode") != "setup" || r.Form.Get("customer") != "cus_controlled" || r.Form.Get("payment_method_types[0]") != "card" || r.Form.Get("setup_intent_data[metadata][actor_id]") != "33" || r.Form.Get("success_url") != c.request.SuccessURL || r.Header.Get("Idempotency-Key") != setupKey(c.charge.MerchantScope, c.request.RequestID, "setup") {
			c.t.Error("invalid hosted setup encoding")
		}
		value = c.setup("open")
	case r.URL.Path == "/v1/checkout/sessions":
		data := []any{c.setup("open")}
		more := false
		if c.mode == "empty" {
			data = nil
		}
		if c.mode == "duplicate" {
			v := c.setup("open")
			v["id"] = "cs_test_second"
			data = append(data, v)
		}
		if c.mode == "truncated" {
			more = true
		}
		value = map[string]any{"object": "list", "data": data, "has_more": more}
	case r.URL.Path == "/v1/checkout/sessions/cs_test_controlled":
		value = c.setup("complete")
		if c.mode == "setup_expired" {
			value.(map[string]any)["expires_at"] = 1
		}
		if c.mode == "setup_incomplete" {
			value.(map[string]any)["status"] = "open"
		}
		if c.mode == "setup_identity" {
			value.(map[string]any)["client_reference_id"] = "different-nonce-12345678"
		}
		if c.mode == "setup_currency" {
			value.(map[string]any)["currency"] = "cny"
		}
	case r.URL.Path == "/v1/setup_intents/seti_controlled":
		value = map[string]any{"id": "seti_controlled", "object": "setup_intent", "status": "succeeded", "usage": "off_session", "customer": "cus_controlled", "payment_method": "pm_controlled", "livemode": c.live, "metadata": setupMetadata(c.request)}
		if c.mode == "usage" {
			value.(map[string]any)["usage"] = "on_session"
		}
		if c.mode == "si_metadata" {
			value.(map[string]any)["metadata"] = map[string]string{"tenant_id": "999"}
		}
	case r.URL.Path == "/v1/customers/cus_controlled/payment_methods/pm_controlled":
		customer := "cus_controlled"
		if c.mode == "wrong_customer" {
			customer = "cus_other"
		}
		value = map[string]any{"id": "pm_controlled", "object": "payment_method", "type": "card", "customer": customer, "livemode": c.live, "card": map[string]any{"brand": "visa", "last4": "4242", "exp_month": 12, "exp_year": time.Now().Year() + 2}}
	case r.URL.Path == "/v1/payment_intents" && r.Method == http.MethodPost:
		c.posts++
		_ = r.ParseForm()
		c.key = r.Header.Get("Idempotency-Key")
		if c.key != c.charge.IdempotencyKey || r.Form.Get("confirm") != "true" || r.Form.Get("off_session") != "true" || r.Form.Get("amount") != "1200" || r.Form.Get("metadata[idempotency_key]") != c.key || r.Form.Get("metadata[period_end]") != c.charge.PeriodEnd.Format(time.RFC3339Nano) {
			c.t.Error("financial request not frozen")
		}
		if c.mode == "timeout" {
			time.Sleep(80 * time.Millisecond)
		}
		if c.mode == "lost_ack" {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				c.t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		if c.mode == "server_error" {
			w.WriteHeader(http.StatusServiceUnavailable)
			value = map[string]any{"error": map[string]string{"message": "SECRET_PROVIDER_BODY"}}
		} else {
			value = c.intent("pi_controlled")
		}
	case r.URL.Path == "/v1/payment_intents":
		if r.Method != http.MethodGet || r.URL.Query().Get("customer") != "cus_controlled" {
			c.t.Error("query not customer bound GET")
		}
		data := []any{c.intent("pi_controlled")}
		more := false
		if c.mode == "empty" {
			data = nil
		}
		if c.mode == "duplicate" {
			data = append(data, c.intent("pi_second"))
		}
		if c.mode == "truncated" {
			more = true
		}
		value = map[string]any{"object": "list", "data": data, "has_more": more}
	case r.URL.Path == "/v1/payment_intents/pi_controlled":
		value = c.intent("pi_controlled")
	default:
		c.t.Errorf("unexpected provider request %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
		value = map[string]string{"error": "unexpected"}
	}
	_ = json.NewEncoder(w).Encode(value)
}
func (c *stripeContract) postCount() int      { c.mu.Lock(); defer c.mu.Unlock(); return c.posts }
func (c *stripeContract) setMode(mode string) { c.mu.Lock(); defer c.mu.Unlock(); c.mode = mode }
func merchantForTest(t *testing.T, a *StripeRecurring) payment.Merchant {
	t.Helper()
	m, _, err := a.registry.Load(context.Background(), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func TestStripeRecurringHostedContract(t *testing.T) {
	a, _, c := newStripeContract(t)
	m := merchantForTest(t, a)
	evidence, err := a.VerifyMerchant(context.Background(), m)
	if err != nil || evidence.Reference == "" || time.Until(evidence.ExpiresAt) > 16*time.Minute {
		t.Fatal(evidence, err)
	}
	session, err := a.StartAuthorization(context.Background(), m, c.request)
	if err != nil || session.Reference != "cs_test_controlled" || c.postCount() != 2 {
		t.Fatal(session, err, c.postCount())
	}
	before := c.postCount()
	lookup, err := a.LookupAuthorization(context.Background(), m, c.request)
	if err != nil || lookup != session || c.postCount() != before {
		t.Fatal(lookup, err)
	}
	auth, err := a.VerifyAuthorization(context.Background(), m, session.Reference)
	if err != nil || auth.ActorID != 33 || auth.RequestID != c.request.RequestID || auth.TenantID != 11 || auth.SubscriptionID != 22 || auth.Label != "visa •••• 4242" || strings.Contains(auth.Reference, "sk_test") || strings.Contains(auth.Reference, "4242") {
		t.Fatal(auth, err)
	}
	c.charge.AuthorizationReference = auth.Reference
	outcome, err := a.Charge(context.Background(), c.charge)
	if err != nil || outcome.Status != ChargeSucceeded || outcome.AmountCents != 1200 {
		t.Fatal(outcome, err)
	}
}
func TestStripeRecurringAmbiguousPostUsesReadOnlyRecovery(t *testing.T) {
	for _, mode := range []string{"lost_ack", "timeout", "server_error", "processing", "requires_action", "wrong_amount", "wrong_tenant"} {
		t.Run(mode, func(t *testing.T) {
			a, st, c := newStripeContract(t)
			c.setMode(mode)
			if mode == "timeout" {
				a.registry.Client.Timeout = 20 * time.Millisecond
			}
			outcome, err := a.Charge(context.Background(), c.charge)
			if err != nil || outcome.Status != ChargeUncertain || c.postCount() != 1 {
				t.Fatal(outcome, err, c.postCount())
			}
			c.setMode("")
			a.registry.Client.Timeout = time.Second
			st.row.Enabled = false
			c.mu.Lock()
			c.capability = false
			c.mu.Unlock() // Revocation prevents new charges, not historical receipt lookup.
			before := c.postCount()
			outcome, err = a.Query(context.Background(), c.charge)
			if err != nil || outcome.Status != ChargeSucceeded || c.postCount() != before {
				t.Fatal(outcome, err, c.postCount())
			}
		})
	}
}
func TestStripeRecurringRefusesBeforeFinancialPost(t *testing.T) {
	for _, mode := range []string{"wrong_customer", "disabled", "account", "environment", "tenant", "amount", "currency", "bad_reference", "expired", "limits"} {
		t.Run(mode, func(t *testing.T) {
			a, st, c := newStripeContract(t)
			req := c.charge
			switch mode {
			case "wrong_customer":
				c.setMode(mode)
			case "limits":
				st.row.CheckoutLimits = nil
			case "disabled":
				st.row.Enabled = false
			case "account":
				c.account = "acct_other"
			case "environment":
				c.live = true
			case "tenant":
				req.TenantID++
			case "amount":
				req.AmountCents = -1
			case "currency":
				req.Currency = "EUR"
			case "bad_reference":
				req.AuthorizationReference = "{}"
			case "expired":
				ref := c.reference
				ref.Expires = 1
				raw, _ := json.Marshal(ref)
				req.AuthorizationReference = string(raw)
			}
			got, err := a.Charge(context.Background(), req)
			if err != nil || got.Status != ChargeFailed || c.postCount() != 0 {
				t.Fatal(got, err, c.postCount())
			}
		})
	}
}
func TestStripeRecurringIncompleteQueryNeverMeansAbsent(t *testing.T) {
	for _, mode := range []string{"empty", "truncated", "duplicate", "wrong_amount", "wrong_tenant", "processing", "requires_action"} {
		t.Run(mode, func(t *testing.T) {
			a, _, c := newStripeContract(t)
			c.setMode(mode)
			got, err := a.Query(context.Background(), c.charge)
			if err != nil || got.Status != ChargeUncertain || c.postCount() != 0 {
				t.Fatal(got, err, c.postCount())
			}
		})
	}
}
func TestStripeRecurringHostedLookupUncertainAndReturnOrigin(t *testing.T) {
	for _, mode := range []string{"empty", "truncated", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			a, _, c := newStripeContract(t)
			c.setMode(mode)
			_, err := a.LookupAuthorization(context.Background(), merchantForTest(t, a), c.request)
			if !errors.Is(err, ErrHostedAuthorizationUncertain) || c.postCount() != 0 {
				t.Fatal(err, c.postCount())
			}
		})
	}
	a, _, c := newStripeContract(t)
	req := c.request
	req.SuccessURL = "https://attacker.example.test/billing"
	if _, err := a.StartAuthorization(context.Background(), merchantForTest(t, a), req); err == nil || c.postCount() != 0 {
		t.Fatal("foreign return accepted")
	}
	for _, raw := range []string{"javascript:alert(1)", "https://checkout.stripe.com.attacker.test/c/pay/id", "https://user@checkout.stripe.com/c/pay/id", "http://checkout.stripe.com/c/pay/id", "https://checkout.stripe.com:444/c/pay/id"} {
		v := stripeSetup{Status: "open", URL: raw, Expires: time.Now().Add(time.Hour).Unix()}
		if _, err := hostedSession(v); err == nil {
			t.Fatal("unsafe hosted URL accepted", raw)
		}
	}
}
func TestStripeRecurringDefinitiveDecline(t *testing.T) {
	a, _, c := newStripeContract(t)
	c.setMode("decline")
	got, err := a.Charge(context.Background(), c.charge)
	if err != nil || got.Status != ChargeFailed || c.postCount() != 1 {
		t.Fatal(got, err)
	}
}

func TestStripeRecurringAuthorizationProviderBindings(t *testing.T) {
	for _, mode := range []string{"setup_expired", "setup_incomplete", "setup_identity", "setup_currency", "usage", "si_metadata", "wrong_customer", "environment", "account"} {
		t.Run(mode, func(t *testing.T) {
			a, _, c := newStripeContract(t)
			m := merchantForTest(t, a)
			c.setMode(mode)
			if mode == "environment" {
				c.live = true
			}
			if mode == "account" {
				c.account = "acct_other"
			}
			if _, err := a.VerifyAuthorization(context.Background(), m, "cs_test_controlled"); err == nil {
				t.Fatal("inconsistent provider authorization accepted")
			}
			if c.postCount() != 0 {
				t.Fatal("verification created side effect")
			}
		})
	}
}

func TestStripeRecurringTransportRefusalsBeforePost(t *testing.T) {
	for _, mode := range []string{"redirect", "oversized", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			a, _, c := newStripeContract(t)
			c.setMode(mode)
			result, err := a.Charge(context.Background(), c.charge)
			if err != nil || result.Status != ChargeFailed || c.postCount() != 0 {
				t.Fatal(result, err, c.postCount())
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if len(c.calls) != 2 {
				t.Fatal("redirect followed or unexpected provider request", c.calls)
			}
		})
	}
}
