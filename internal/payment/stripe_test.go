package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sign(secret string, ts int64, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhook(t *testing.T) {
	s := NewStripe("sk", "whsec")
	payload := []byte(`{"type":"checkout.session.completed"}`)
	now := time.Now().Unix()
	if err := s.VerifyWebhook(payload, sign("whsec", now, payload), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyWebhook(payload, sign("wrong", now, payload), 5*time.Minute); err == nil {
		t.Fatal("wrong signature accepted")
	}
	if err := s.VerifyWebhook(payload, sign("whsec", now-3600, payload), 5*time.Minute); err == nil {
		t.Fatal("old signature accepted")
	}
}

func TestParseAndVerifyWebhook(t *testing.T) {
	s := NewStripe("sk", "whsec")

	t.Run("paid checkout session succeeds", func(t *testing.T) {
		payload := []byte(`{
			"id": "evt_123",
			"type": "checkout.session.completed",
			"data": {
				"object": {
					"id": "cs_test_123",
					"payment_intent": "pi_test_999",
					"payment_status": "paid",
					"amount_total": 9900,
					"currency": "cny",
					"client_reference_id": "ORD-20261023-01",
					"metadata": {
						"order_id": "42"
					}
				}
			}
		}`)
		now := time.Now().Unix()
		header := http.Header{}
		header.Set("Stripe-Signature", sign("whsec", now, payload))

		event, err := s.ParseAndVerifyWebhook(context.Background(), header, payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if event.Provider != ProviderStripe {
			t.Errorf("expected provider stripe, got %s", event.Provider)
		}
		if event.OrderID != 42 {
			t.Errorf("expected orderID 42, got %d", event.OrderID)
		}
		if event.OrderNo != "ORD-20261023-01" {
			t.Errorf("expected orderNo ORD-20261023-01, got %s", event.OrderNo)
		}
		if event.ProviderTxnID != "pi_test_999" {
			t.Errorf("expected providerTxnID pi_test_999, got %s", event.ProviderTxnID)
		}
		if event.AmountCents != 9900 {
			t.Errorf("expected amount 9900, got %d", event.AmountCents)
		}
		if event.Currency != "CNY" {
			t.Errorf("expected currency CNY, got %s", event.Currency)
		}
		if event.Status != StatusSucceeded {
			t.Errorf("expected status succeeded, got %s", event.Status)
		}
	})

	t.Run("unpaid checkout session rejected", func(t *testing.T) {
		payload := []byte(`{
			"id": "evt_456",
			"type": "checkout.session.completed",
			"data": {
				"object": {
					"id": "cs_test_456",
					"payment_status": "unpaid",
					"amount_total": 5000,
					"currency": "cny",
					"metadata": {
						"order_id": "43"
					}
				}
			}
		}`)
		now := time.Now().Unix()
		header := http.Header{}
		header.Set("Stripe-Signature", sign("whsec", now, payload))

		_, err := s.ParseAndVerifyWebhook(context.Background(), header, payload)
		if !errors.Is(err, ErrPaymentNotPaid) {
			t.Fatalf("expected ErrPaymentNotPaid, got %v", err)
		}
	})
}

func TestCreateCheckoutSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/checkout/sessions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("missing bearer token")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.PostForm.Get("payment_intent_data[metadata][order_id]") != "42" {
			t.Error("payment intent metadata was not copied by checkout request")
		}
		if r.PostForm.Get("metadata[order_id]") != "42" {
			t.Errorf("order metadata = %q", r.PostForm.Get("metadata[order_id]"))
		}
		_, _ = w.Write([]byte(`{"id":"cs_1","url":"https://checkout.stripe.test/cs_1"}`))
	}))
	defer srv.Close()
	s := NewStripe("sk", "whsec")
	s.CheckoutLimits = map[string]AmountLimit{"CNY": {MinCents: 1, MaxCents: 99999999, SettlementCurrency: "CNY", Source: "merchant_contract"}}
	s.Base = srv.URL
	got, err := s.CreateCheckoutSession(context.Background(), 42, "ORD-1", 1000, "cny", "https://a", "https://b")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://checkout.stripe.test/cs_1" {
		t.Fatalf("url = %s", got.URL)
	}
}

func TestQueryPaymentAndRefund(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/cs_paid") {
			_, _ = w.Write([]byte(`{
				"id": "cs_paid",
				"payment_status": "paid",
				"amount_total": 5000,
				"currency": "cny"
			}`))
			return
		}
		if r.URL.Path == "/v1/refunds" && r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.PostForm.Get("payment_intent") != "pi_123" {
				t.Errorf("expected payment_intent pi_123, got %s", r.PostForm.Get("payment_intent"))
			}
			_, _ = w.Write([]byte(`{
				"id": "re_123",
				"status": "succeeded"
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := NewStripe("sk", "whsec")
	s.CheckoutLimits = map[string]AmountLimit{"CNY": {MinCents: 1, MaxCents: 99999999, SettlementCurrency: "CNY", Source: "merchant_contract"}}
	s.Base = srv.URL

	// Query
	queryRes, err := s.QueryPayment(context.Background(), "ORD-1", "cs_paid")
	if err != nil {
		t.Fatalf("QueryPayment failed: %v", err)
	}
	if queryRes.Status != StatusSucceeded || queryRes.AmountCents != 5000 {
		t.Errorf("unexpected query result: %+v", queryRes)
	}

	// Refund
	refundRes, err := s.Refund(context.Background(), RefundParams{
		ProviderTxnID: "pi_123",
		AmountCents:   5000,
		RefundNo:      "REF-1",
	})
	if err != nil {
		t.Fatalf("Refund failed: %v", err)
	}
	if refundRes.ProviderRefundID != "re_123" || refundRes.Status != "succeeded" {
		t.Errorf("unexpected refund result: %+v", refundRes)
	}
}

func TestCheckoutConstructedMetadataProducesResolvablePaymentIntent(t *testing.T) {
	var intentOrder string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		intentOrder = r.PostForm.Get("payment_intent_data[metadata][order_id]")
		_, _ = w.Write([]byte(`{"id":"cs_metadata","url":"https://checkout.stripe.test/cs_metadata"}`))
	}))
	defer srv.Close()
	s := NewStripe("test-key", "test-webhook")
	s.CheckoutLimits = map[string]AmountLimit{"CNY": {MinCents: 1, MaxCents: 99999999, SettlementCurrency: "CNY", Source: "merchant_contract"}}
	s.Base = srv.URL
	if _, err := s.CreateCheckoutSession(context.Background(), 42, "ORDER42", 1000, "CNY", "https://panel.test/success", "https://panel.test/cancel"); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"id": "evt_constructed", "type": "payment_intent.succeeded", "data": map[string]any{"object": map[string]any{"id": "pi_constructed", "status": "succeeded", "amount_received": 1000, "currency": "cny", "metadata": map[string]string{"order_id": intentOrder}}}})
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	header.Set("Stripe-Signature", sign("test-webhook", time.Now().Unix(), payload))
	event, err := s.ParseAndVerifyWebhook(context.Background(), header, payload)
	if err != nil || event.OrderID != 42 || event.ProviderTxnID != "pi_constructed" {
		t.Fatalf("constructed PI metadata was not usable: %+v %v", event, err)
	}
}

func TestStripeCloseAndQueryUseStableSessionAndPaymentIdentity(t *testing.T) {
	closed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/checkout/sessions/cs_close/expire":
			if r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") != "urmail-close-cs_close" {
				t.Error("unstable session close request")
			}
			closed = true
			_, _ = w.Write([]byte(`{"id":"cs_close","status":"expired"}`))
		case "/v1/checkout/sessions/cs_paid":
			_, _ = w.Write([]byte(`{"id":"cs_paid","payment_intent":"pi_original","payment_status":"paid","amount_total":1000,"currency":"cny"}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	s := NewStripe("test-key", "test-webhook")
	s.CheckoutLimits = map[string]AmountLimit{"CNY": {MinCents: 1, MaxCents: 99999999, SettlementCurrency: "CNY", Source: "merchant_contract"}}
	s.Base = srv.URL
	if err := s.CloseCheckout(context.Background(), "ORDER42", "cs_close"); err != nil || !closed {
		t.Fatalf("close %v %v", closed, err)
	}
	status, err := s.QueryPayment(context.Background(), "ORDER42", "cs_paid")
	if err != nil || status.ProviderTxnID != "pi_original" || status.Status != StatusSucceeded {
		t.Fatalf("query lost original refundable payment identity: %+v %v", status, err)
	}
}
