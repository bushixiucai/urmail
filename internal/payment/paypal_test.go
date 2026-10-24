package payment

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func makeSignatureHeaders(sig string) http.Header {
	h := http.Header{}
	h.Set("Paypal-Auth-Algo", "SHA256withRSA")
	h.Set("Paypal-Cert-Url", "https://api.sandbox.paypal.com/v1/notifications/certs/CERT-123")
	h.Set("Paypal-Transmission-Id", "tx-12345")
	h.Set("Paypal-Transmission-Sig", sig)
	h.Set("Paypal-Transmission-Time", "2026-10-23T08:00:00Z")
	return h
}

func TestPayPal_ProviderInterface(t *testing.T) {
	p := NewPayPal("client-id", "secret", "wh-id")
	var provider Provider = p
	if provider.Name() != ProviderPayPal {
		t.Fatalf("expected %s, got %s", ProviderPayPal, provider.Name())
	}
}

func TestPayPal_AmountHelpers(t *testing.T) {
	// format
	if got := formatPayPalAmount(1050, "USD"); got != "10.50" {
		t.Errorf("expected 10.50, got %s", got)
	}
	if got := formatPayPalAmount(5, "USD"); got != "0.05" {
		t.Errorf("expected 0.05, got %s", got)
	}
	if got := formatPayPalAmount(1000, "JPY"); got != "1000" {
		t.Errorf("expected 1000, got %s", got)
	}

	// parse
	cents, err := parsePayPalAmount("10.50", "USD")
	if err != nil || cents != 1050 {
		t.Errorf("expected 1050, got %d (err: %v)", cents, err)
	}
	cents, err = parsePayPalAmount("1000", "JPY")
	if err != nil || cents != 1000 {
		t.Errorf("expected 1000, got %d (err: %v)", cents, err)
	}
	cents, err = parsePayPalAmount("", "USD")
	if err == nil {
		t.Errorf("expected error for empty amount, got cents %d", cents)
	}

	// references
	id, no := parsePayPalReference("ORD-1|42", "", "")
	if id != 42 || no != "ORD-1" {
		t.Errorf("expected 42, ORD-1, got %d, %s", id, no)
	}
	id, no = parsePayPalReference("42", "ORD-2", "")
	if id != 42 || no != "ORD-2" {
		t.Errorf("expected 42, ORD-2, got %d, %s", id, no)
	}
	id, no = parsePayPalReference("ORD-3", "", "INV-3")
	if id != 0 || no != "ORD-3" {
		t.Errorf("expected 0, ORD-3, got %d, %s", id, no)
	}
}

func TestPayPal_TokenFetchAndCaching(t *testing.T) {
	var tokenCalls int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			atomic.AddInt32(&tokenCalls, 1)

			user, pass, ok := r.BasicAuth()
			if !ok || user != "test-client" || pass != "test-secret" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "client_credentials" {
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-token-xyz","token_type":"Bearer","expires_in":3600}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := NewPayPal("test-client", "test-secret", "wh-1")
	p.Base = srv.URL

	// First fetch
	token1, err := p.getAccessToken(context.Background())
	if err != nil {
		t.Fatalf("first token fetch failed: %v", err)
	}
	if token1 != "mock-token-xyz" {
		t.Fatalf("unexpected token: %s", token1)
	}

	// Second fetch should use cached token
	token2, err := p.getAccessToken(context.Background())
	if err != nil {
		t.Fatalf("second token fetch failed: %v", err)
	}
	if token2 != "mock-token-xyz" {
		t.Fatalf("unexpected token: %s", token2)
	}

	if calls := atomic.LoadInt32(&tokenCalls); calls != 1 {
		t.Fatalf("expected 1 token call, got %d", calls)
	}

	// Test auth failure
	pBad := NewPayPal("wrong-client", "wrong-secret", "wh-1")
	pBad.Base = srv.URL
	_, err = pBad.getAccessToken(context.Background())
	if err == nil {
		t.Fatal("expected error on bad credentials, got nil")
	}
}

func TestPayPal_CreateCheckout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-token","token_type":"Bearer","expires_in":3600}`))
			return
		}

		if r.URL.Path == "/v2/checkout/orders" && r.Method == http.MethodPost {
			if r.Header.Get("Authorization") != "Bearer mock-token" {
				http.Error(w, "missing auth", http.StatusUnauthorized)
				return
			}
			if r.Header.Get("PayPal-Request-Id") != "urmail-checkout-ORD-001" {
				t.Errorf("unexpected idempotency key: %s", r.Header.Get("PayPal-Request-Id"))
			}

			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("unmarshal req: %v", err)
			}
			if req["intent"] != "CAPTURE" {
				t.Errorf("expected intent CAPTURE, got %v", req["intent"])
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id": "5O12345678",
				"status": "CREATED",
				"links": [
					{"href": "https://api-m.paypal.com/v2/checkout/orders/5O12345678", "rel": "self", "method": "GET"},
					{"href": "https://www.paypal.com/checkoutnow?token=5O12345678", "rel": "approve", "method": "GET"}
				]
			}`))
			return
		}

		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := NewPayPal("client", "secret", "wh-1")
	p.Base = srv.URL

	res, err := p.CreateCheckout(context.Background(), CheckoutParams{
		TenantID:    1,
		OrderID:     42,
		OrderNo:     "ORD-001",
		AmountCents: 2999,
		Currency:    "USD",
		SuccessURL:  "https://example.com/success",
		CancelURL:   "https://example.com/cancel",
	})
	if err != nil {
		t.Fatalf("CreateCheckout failed: %v", err)
	}
	if res.SessionID != "5O12345678" {
		t.Errorf("expected session ID 5O12345678, got %s", res.SessionID)
	}
	if res.URL != "https://www.paypal.com/checkoutnow?token=5O12345678" {
		t.Errorf("expected approve URL, got %s", res.URL)
	}
}

func TestPayPal_ParseAndVerifyWebhook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-token","token_type":"Bearer","expires_in":3600}`))
			return
		}

		if r.URL.Path == "/v1/notifications/verify-webhook-signature" {
			body, _ := io.ReadAll(r.Body)
			var req struct {
				TransmissionSig string `json:"transmission_sig"`
				WebhookID       string `json:"webhook_id"`
			}
			_ = json.Unmarshal(body, &req)

			w.Header().Set("Content-Type", "application/json")
			if req.TransmissionSig == "valid_sig" && req.WebhookID == "wh-test" {
				_, _ = w.Write([]byte(`{"verification_status":"SUCCESS"}`))
			} else {
				_, _ = w.Write([]byte(`{"verification_status":"FAILURE"}`))
			}
			return
		}

		if r.URL.Path == "/v2/checkout/orders/ORD-APP-001/capture" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id": "ORD-APP-001",
				"status": "COMPLETED",
				"purchase_units": [
					{
						"payments": {
							"captures": [
								{
									"id": "CAP-001",
									"status": "COMPLETED",
									"amount": {"currency_code": "USD", "value": "50.00"},
									"create_time": "2026-10-23T08:00:00Z"
								}
							]
						}
					}
				]
			}`))
			return
		}

		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := NewPayPal("client", "secret", "wh-test")
	p.Base = srv.URL

	t.Run("CHECKOUT.ORDER.APPROVED triggers capture and returns completed event", func(t *testing.T) {
		payload := []byte(`{
			"id": "WH-EVT-001",
			"event_type": "CHECKOUT.ORDER.APPROVED",
			"resource": {
				"id": "ORD-APP-001",
				"status": "APPROVED",
				"purchase_units": [
					{
						"reference_id": "ORD-PP-002",
						"custom_id": "102",
						"amount": {"currency_code": "USD", "value": "50.00"}
					}
				]
			}
		}`)

		header := makeSignatureHeaders("valid_sig")
		event, err := p.ParseAndVerifyWebhook(context.Background(), header, payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if event.Provider != ProviderPayPal {
			t.Errorf("provider = %s", event.Provider)
		}
		if event.OrderID != 102 {
			t.Errorf("orderID = %d", event.OrderID)
		}
		if event.OrderNo != "ORD-PP-002" {
			t.Errorf("orderNo = %s", event.OrderNo)
		}
		if event.ProviderTxnID != "CAP-001" {
			t.Errorf("providerTxnID = %s", event.ProviderTxnID)
		}
		if event.AmountCents != 5000 {
			t.Errorf("amountCents = %d", event.AmountCents)
		}
		if event.Currency != "USD" {
			t.Errorf("currency = %s", event.Currency)
		}
		if event.Status != StatusSucceeded {
			t.Errorf("status = %s", event.Status)
		}
	})

	t.Run("CHECKOUT.ORDER.APPROVED already COMPLETED returns succeeded", func(t *testing.T) {
		payload := []byte(`{
			"id": "WH-EVT-002",
			"event_type": "CHECKOUT.ORDER.APPROVED",
			"resource": {
				"id": "ORD-COMPLETED-001",
				"status": "COMPLETED",
				"purchase_units": [
					{
						"reference_id": "ORD-PP-002B",
						"custom_id": "102",
						"amount": {"currency_code": "USD", "value": "30.00"},
						"payments": {
							"captures": [
								{
									"id": "CAP-EXISTING-001",
									"status": "COMPLETED",
									"amount": {"currency_code": "USD", "value": "30.00"}
								}
							]
						}
					}
				]
			}
		}`)

		header := makeSignatureHeaders("valid_sig")
		event, err := p.ParseAndVerifyWebhook(context.Background(), header, payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if event.Status != StatusSucceeded {
			t.Errorf("status = %s", event.Status)
		}
		if event.ProviderTxnID != "CAP-EXISTING-001" {
			t.Errorf("providerTxnID = %s", event.ProviderTxnID)
		}
	})

	t.Run("CHECKOUT.ORDER.APPROVED with status VOIDED rejected", func(t *testing.T) {
		payload := []byte(`{
			"id": "WH-EVT-003",
			"event_type": "CHECKOUT.ORDER.APPROVED",
			"resource": {
				"id": "ORD-VOIDED-001",
				"status": "VOIDED"
			}
		}`)

		header := makeSignatureHeaders("valid_sig")
		_, err := p.ParseAndVerifyWebhook(context.Background(), header, payload)
		if !errors.Is(err, ErrPaymentNotPaid) {
			t.Fatalf("expected ErrPaymentNotPaid, got %v", err)
		}
	})

	t.Run("PAYMENT.CAPTURE.COMPLETED success", func(t *testing.T) {
		payload := []byte(`{
			"id": "WH-EVT-004",
			"event_type": "PAYMENT.CAPTURE.COMPLETED",
			"resource": {
				"id": "CAP-999",
				"status": "COMPLETED",
				"custom_id": "ORD-PP-003|103",
				"amount": {"currency_code": "USD", "value": "15.00"},
				"create_time": "2026-10-23T08:00:00Z"
			}
		}`)

		header := makeSignatureHeaders("valid_sig")
		event, err := p.ParseAndVerifyWebhook(context.Background(), header, payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if event.OrderID != 103 || event.OrderNo != "ORD-PP-003" {
			t.Errorf("unexpected ids: orderID=%d, orderNo=%s", event.OrderID, event.OrderNo)
		}
		if event.ProviderTxnID != "CAP-999" || event.AmountCents != 1500 {
			t.Errorf("unexpected details: txn=%s, cents=%d", event.ProviderTxnID, event.AmountCents)
		}
		if event.Status != StatusSucceeded {
			t.Errorf("status = %s", event.Status)
		}
	})

	t.Run("PAYMENT.CAPTURE.COMPLETED with non-completed status rejected", func(t *testing.T) {
		payload := []byte(`{
			"id": "WH-EVT-005",
			"event_type": "PAYMENT.CAPTURE.COMPLETED",
			"resource": {
				"id": "CAP-PENDING",
				"status": "PENDING",
				"custom_id": "104",
				"amount": {"currency_code": "USD", "value": "10.00"}
			}
		}`)

		header := makeSignatureHeaders("valid_sig")
		_, err := p.ParseAndVerifyWebhook(context.Background(), header, payload)
		if !errors.Is(err, ErrPaymentNotPaid) {
			t.Fatalf("expected ErrPaymentNotPaid, got %v", err)
		}
	})

	t.Run("Invalid signature rejected", func(t *testing.T) {
		payload := []byte(`{"id":"WH-EVT-006","event_type":"PAYMENT.CAPTURE.COMPLETED","resource":{"status":"COMPLETED"}}`)
		header := makeSignatureHeaders("invalid_sig")

		_, err := p.ParseAndVerifyWebhook(context.Background(), header, payload)
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("expected ErrInvalidSignature, got %v", err)
		}
	})

	t.Run("Missing signature headers rejected", func(t *testing.T) {
		payload := []byte(`{"id":"WH-EVT-007"}`)
		header := http.Header{} // empty headers

		_, err := p.ParseAndVerifyWebhook(context.Background(), header, payload)
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("expected ErrInvalidSignature, got %v", err)
		}
	})

	t.Run("Ignored event type passes without failure", func(t *testing.T) {
		payload := []byte(`{
			"id": "WH-EVT-008",
			"event_type": "CUSTOMER.DISPUTE.CREATED",
			"resource": {"id": "D-123"}
		}`)
		header := makeSignatureHeaders("valid_sig")

		evt, err := p.ParseAndVerifyWebhook(context.Background(), header, payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if evt.EventType != "CUSTOMER.DISPUTE.CREATED" {
			t.Errorf("unexpected event type: %s", evt.EventType)
		}
	})
}

func TestPayPal_QueryPayment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-token","token_type":"Bearer","expires_in":3600}`))
			return
		}

		if r.URL.Path == "/v2/checkout/orders/ORD-PAID-001" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id": "ORD-PAID-001",
				"status": "COMPLETED",
				"purchase_units": [
					{
						"amount": {"currency_code": "USD", "value": "19.99"},
						"payments": {
							"captures": [
								{
									"id": "CAP-PAID-001",
									"status": "COMPLETED",
									"amount": {"currency_code": "USD", "value": "19.99"},
									"create_time": "2026-10-23T08:00:00Z"
								}
							]
						}
					}
				]
			}`))
			return
		}

		if r.URL.Path == "/v2/checkout/orders/ORD-PENDING-001" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id": "ORD-PENDING-001",
				"status": "CREATED",
				"purchase_units": [
					{"amount": {"currency_code": "USD", "value": "10.00"}}
				]
			}`))
			return
		}

		if r.URL.Path == "/v2/checkout/orders/CAP-DIRECT-001" {
			http.NotFound(w, r)
			return
		}

		if r.URL.Path == "/v2/payments/captures/CAP-DIRECT-001" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id": "CAP-DIRECT-001",
				"status": "COMPLETED",
				"amount": {"currency_code": "EUR", "value": "25.00"},
				"create_time": "2026-10-23T08:00:00Z"
			}`))
			return
		}

		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := NewPayPal("client", "secret", "wh-1")
	p.Base = srv.URL

	// 1. Order completed
	res, err := p.QueryPayment(context.Background(), "ORD-1", "ORD-PAID-001")
	if err != nil {
		t.Fatalf("QueryPayment order completed failed: %v", err)
	}
	if res.Status != StatusSucceeded || res.AmountCents != 1999 || res.ProviderTxnID != "CAP-PAID-001" {
		t.Errorf("unexpected query result: %+v", res)
	}

	// 2. Order pending
	res, err = p.QueryPayment(context.Background(), "ORD-2", "ORD-PENDING-001")
	if err != nil {
		t.Fatalf("QueryPayment order pending failed: %v", err)
	}
	if res.Status != StatusPending || res.AmountCents != 1000 {
		t.Errorf("unexpected query result: %+v", res)
	}

	// 3. Fallback to capture
	res, err = p.QueryPayment(context.Background(), "ORD-3", "CAP-DIRECT-001")
	if err != nil {
		t.Fatalf("QueryPayment capture fallback failed: %v", err)
	}
	if res.Status != StatusSucceeded || res.AmountCents != 2500 || res.Currency != "EUR" {
		t.Errorf("unexpected query result: %+v", res)
	}

	// 4. Empty providerTxnID
	_, err = p.QueryPayment(context.Background(), "ORD-4", "")
	if err == nil {
		t.Fatal("expected error on empty providerTxnID")
	}
}

func TestPayPal_Refund(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-token","token_type":"Bearer","expires_in":3600}`))
			return
		}

		if r.URL.Path == "/v2/payments/captures/CAP-REF-001/refund" && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)

			if req["invoice_id"] != "REF-100" {
				t.Errorf("expected invoice_id REF-100, got %v", req["invoice_id"])
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"REF-ID-001","status":"COMPLETED"}`))
			return
		}

		// Fallback scenario: capture ID not found directly, but order has capture
		if r.URL.Path == "/v2/payments/captures/ORD-FALLBACK-001/refund" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/v2/checkout/orders/ORD-FALLBACK-001" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id": "ORD-FALLBACK-001",
				"purchase_units": [
					{
						"payments": {
							"captures": [{"id": "CAP-FROM-ORDER-001"}]
						}
					}
				]
			}`))
			return
		}
		if r.URL.Path == "/v2/payments/captures/CAP-FROM-ORDER-001/refund" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"REF-ID-002","status":"PENDING"}`))
			return
		}

		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := NewPayPal("client", "secret", "wh-1")
	p.Base = srv.URL

	// 1. Direct capture refund
	res, err := p.Refund(context.Background(), RefundParams{
		ProviderTxnID: "CAP-REF-001",
		RefundNo:      "REF-100",
		AmountCents:   500,
		Currency:      "USD",
		Reason:        "user request",
	})
	if err != nil {
		t.Fatalf("Refund failed: %v", err)
	}
	if res.ProviderRefundID != "REF-ID-001" || res.Status != "succeeded" {
		t.Errorf("unexpected refund result: %+v", res)
	}

	// 2. Order ID fallback refund
	res, err = p.Refund(context.Background(), RefundParams{
		ProviderTxnID: "ORD-FALLBACK-001",
		RefundNo:      "REF-101",
		AmountCents:   500,
		Currency:      "USD",
	})
	if err != nil {
		t.Fatalf("Fallback refund failed: %v", err)
	}
	if res.ProviderRefundID != "REF-ID-002" || res.Status != "processing" {
		t.Errorf("unexpected refund result: %+v", res)
	}

	// 3. Missing providerTxnID
	_, err = p.Refund(context.Background(), RefundParams{})
	if err == nil {
		t.Fatal("expected error on empty ProviderTxnID")
	}
}

func TestPayPal_ParseAmountRejectsInvalid(t *testing.T) {
	if _, err := parsePayPalAmount("", "USD"); err == nil {
		t.Fatal("expected error for empty amount")
	}
	if _, err := parsePayPalAmount("abc", "USD"); err == nil {
		t.Fatal("expected error for malformed amount")
	}
	if _, err := parsePayPalAmount("10.50", ""); err == nil {
		t.Fatal("expected error for missing currency")
	}
	if _, err := parsePayPalAmount("  ", "USD"); err == nil {
		t.Fatal("expected error for whitespace-only amount")
	}
}
