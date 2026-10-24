package payment

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Stripe webhook event mapping
// ---------------------------------------------------------------------------

func stripeSigned(t *testing.T, s *Stripe, payload []byte) (WebhookEvent, error) {
	t.Helper()
	header := http.Header{}
	header.Set("Stripe-Signature", sign("whsec", time.Now().Unix(), payload))
	return s.ParseAndVerifyWebhook(context.Background(), header, payload)
}

func TestStripeAsyncPaymentSucceededSettles(t *testing.T) {
	s := NewStripe("sk", "whsec")
	payload := []byte(`{
		"id": "evt_async_ok",
		"type": "checkout.session.async_payment_succeeded",
		"data": {"object": {
			"id": "cs_async_1",
			"payment_intent": "pi_async_1",
			"payment_status": "paid",
			"amount_total": 8800,
			"currency": "cny",
			"client_reference_id": "ORD-ASYNC-1",
			"metadata": {"order_id": "77"}
		}}
	}`)
	ev, err := stripeSigned(t, s, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.EventType != "checkout.session.async_payment_succeeded" {
		t.Errorf("event type = %q", ev.EventType)
	}
	if ev.EventID != "evt_async_ok" || ev.ProviderTxnID != "pi_async_1" ||
		ev.AmountCents != 8800 || ev.Currency != "CNY" || ev.OrderID != 77 ||
		ev.OrderNo != "ORD-ASYNC-1" || ev.Status != StatusSucceeded {
		t.Errorf("unexpected settlement event: %+v", ev)
	}
}

func TestStripeAsyncPaymentFailedIsDistinguishable(t *testing.T) {
	s := NewStripe("sk", "whsec")
	for _, typ := range []string{"checkout.session.async_payment_failed", "checkout.session.expired"} {
		payload := []byte(`{"id":"evt_term","type":"` + typ + `","data":{"object":{
			"id":"cs_term","payment_intent":"pi_term","amount_total":8800,
			"currency":"cny","client_reference_id":"ORD-TERM","metadata":{"order_id":"78"}}}}`)
		ev, err := stripeSigned(t, s, payload)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", typ, err)
		}
		if ev.EventType != typ {
			t.Errorf("event type = %q, want %q", ev.EventType, typ)
		}
		if ev.Status != StatusFailed {
			t.Errorf("status = %q, want failed", ev.Status)
		}
		if ev.OrderID != 78 || ev.ProviderTxnID != "pi_term" || ev.AmountCents != 8800 || ev.EventID != "evt_term" {
			t.Errorf("identity fields not populated: %+v", ev)
		}
	}
}

func TestStripePaymentIntentSucceededMapsOrderViaMetadata(t *testing.T) {
	s := NewStripe("sk", "whsec")
	payload := []byte(`{
		"id": "evt_pi_ok",
		"type": "payment_intent.succeeded",
		"data": {"object": {
			"id": "pi_direct_1",
			"amount": 6600,
			"currency": "cny",
			"metadata": {"order_id": "99"}
		}}
	}`)
	ev, err := stripeSigned(t, s, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.EventType != "payment_intent.succeeded" || ev.OrderID != 99 ||
		ev.ProviderTxnID != "pi_direct_1" || ev.AmountCents != 6600 ||
		ev.Status != StatusSucceeded {
		t.Errorf("unexpected event: %+v", ev)
	}

	// Without any order metadata the event cannot be mapped to our order.
	payload = []byte(`{"id":"evt_pi_nometa","type":"payment_intent.succeeded","data":{"object":{
		"id":"pi_direct_2","amount":6600,"currency":"cny"}}}`)
	if _, err := stripeSigned(t, s, payload); !errors.Is(err, ErrInvalidPayload) {
		t.Errorf("expected ErrInvalidPayload for unmappable payment_intent, got %v", err)
	}
}

func TestStripeMissingPaymentStatusIsNotPaid(t *testing.T) {
	s := NewStripe("sk", "whsec")
	payload := []byte(`{"id":"evt_nostatus","type":"checkout.session.completed","data":{"object":{
		"id":"cs_nostatus","amount_total":5000,"currency":"cny","metadata":{"order_id":"42"}}}}`)
	_, err := stripeSigned(t, s, payload)
	if !errors.Is(err, ErrPaymentNotPaid) {
		t.Fatalf("expected ErrPaymentNotPaid for missing payment_status, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Stripe refund idempotency keys
// ---------------------------------------------------------------------------

func TestStripeRefundRequiresRefundNoAndBuildsDistinctKeys(t *testing.T) {
	var mu atomic.Int32
	var keys []string
	var refundNos []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/refunds" && r.Method == http.MethodPost {
			_ = r.ParseForm()
			mu.Add(1)
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			refundNos = append(refundNos, r.PostForm.Get("metadata[refund_no]"))
			_, _ = w.Write([]byte(`{"id":"re_` + strconv.Itoa(int(mu.Load())) + `","status":"succeeded"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := NewStripe("sk", "whsec")
	s.Base = srv.URL

	// Empty RefundNo is rejected before any HTTP call.
	if _, err := s.Refund(context.Background(), RefundParams{ProviderTxnID: "pi_1", AmountCents: 100, RefundNo: ""}); err == nil {
		t.Fatal("expected error for empty RefundNo")
	}
	if calls := mu.Load(); calls != 0 {
		t.Fatalf("empty RefundNo must not reach the API, got %d calls", calls)
	}

	for _, no := range []string{"REF-A", "REF-B"} {
		if _, err := s.Refund(context.Background(), RefundParams{ProviderTxnID: "pi_1", AmountCents: 100, RefundNo: no}); err != nil {
			t.Fatalf("refund %s: %v", no, err)
		}
	}
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("expected distinct idempotency keys, got %v", keys)
	}
	for i, no := range refundNos {
		if no == "" {
			t.Fatalf("refund %d sent no metadata[refund_no]", i)
		}
		if keys[i] != "urmail-refund-"+no {
			t.Errorf("key %q does not derive from refund no %q", keys[i], no)
		}
	}
}

// ---------------------------------------------------------------------------
// Alipay yuan-to-cents conversion
// ---------------------------------------------------------------------------

func TestAlipayParseYuanToCentsBoundaries(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"12.34", 1234, false},
		{"0.05", 5, false},
		{"100.", 10000, false},
		{"100", 10000, false},
		{"100.999", 0, true},                // >2 decimals must be rejected, not truncated
		{"9223372036854775807.00", 0, true}, // must not overflow/wrap to -100
		{"92233720368547758.07", 9223372036854775807, false},
		{"-12.34", -1234, false},
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := parseYuanToCents(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseYuanToCents(%q) = %d, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseYuanToCents(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseYuanToCents(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Alipay refund amount validation
// ---------------------------------------------------------------------------

func TestAlipayRefundValidatesAmount(t *testing.T) {
	// These validations run before any network call; a nil client is fine.
	a := NewAlipay("", nil, nil)

	if _, err := a.Refund(context.Background(), RefundParams{OrderNo: "O", AmountCents: 0}); err == nil {
		t.Error("expected error for zero refund amount")
	}
	if _, err := a.Refund(context.Background(), RefundParams{OrderNo: "O", AmountCents: -100}); err == nil {
		t.Error("expected error for negative refund amount")
	}
	_, err := a.Refund(context.Background(), RefundParams{
		OrderNo: "O", AmountCents: 6000, OriginalAmountCents: 5000,
	})
	if err == nil {
		t.Error("expected error when refund exceeds original total")
	}

	// Within the original total passes validation and fails later only on the
	// missing private key (i.e. validation itself did not reject it).
	_, err = a.Refund(context.Background(), RefundParams{
		OrderNo: "O", AmountCents: 5000, OriginalAmountCents: 6000,
	})
	if err == nil || err.Error() != "alipay merchant private key not configured" {
		t.Errorf("expected key-not-configured error (validation passed), got %v", err)
	}
}

// ---------------------------------------------------------------------------
// WeChat refund original-total requirement
// ---------------------------------------------------------------------------

func TestWeChatRefundRequiresOriginalAmount(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"refund_id":"5000009999","status":"PROCESSING"}`))
	}))
	defer srv.Close()

	wc := NewWeChatPay("1900000109", "wx1234567890", "12345678901234567890123456789012", "CERT", nil, nil)
	wc.Base = srv.URL

	// Partial refund with missing original amount must be rejected, not
	// silently turned into a full refund.
	if _, err := wc.Refund(context.Background(), RefundParams{
		OrderNo: "ORD-WX-P1", RefundNo: "REF-WX-P1", AmountCents: 2000, Currency: "CNY",
	}); err == nil {
		t.Fatal("expected error for missing OriginalAmountCents on partial refund")
	}
	if calls != 0 {
		t.Fatalf("rejected refund must not reach the API, got %d calls", calls)
	}

	// Refund larger than the known total is rejected too.
	if _, err := wc.Refund(context.Background(), RefundParams{
		OrderNo: "ORD-WX-P2", RefundNo: "REF-WX-P2",
		AmountCents: 7000, OriginalAmountCents: 6000, Currency: "CNY",
	}); err == nil {
		t.Fatal("expected error when refund exceeds original total")
	}
	if calls != 0 {
		t.Fatalf("rejected over-refund must not reach the API, got %d calls", calls)
	}
}

// ---------------------------------------------------------------------------
// PayPal webhook dedup before capture and order resolution
// ---------------------------------------------------------------------------

func paypalWebhookServer(t *testing.T, captureCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/oauth2/token":
			_, _ = w.Write([]byte(`{"access_token":"mock-token","token_type":"Bearer","expires_in":3600}`))
		case r.URL.Path == "/v1/notifications/verify-webhook-signature":
			_, _ = w.Write([]byte(`{"verification_status":"SUCCESS"}`))
		case r.URL.Path == "/v2/checkout/orders/ORD-APP-DUP/capture" && r.Method == http.MethodPost:
			captureCalls.Add(1)
			_, _ = w.Write([]byte(`{"id":"ORD-APP-DUP","status":"COMPLETED","purchase_units":[{"payments":{"captures":[{"id":"CAP-DUP","status":"COMPLETED","amount":{"currency_code":"USD","value":"50.00"}}]}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestPayPalDedupRunsBeforeCapture(t *testing.T) {
	var captureCalls atomic.Int32
	srv := paypalWebhookServer(t, &captureCalls)
	defer srv.Close()

	p := NewPayPal("client", "secret", "wh-test")
	p.Base = srv.URL

	seen := map[string]bool{}
	var seenOrder []string
	p.EventSeen = func(_ context.Context, eventID string) (bool, error) {
		if seen[eventID] {
			return true, nil
		}
		seen[eventID] = true
		seenOrder = append(seenOrder, eventID)
		return false, nil
	}

	payload := []byte(`{
		"id": "WH-EVT-DUP",
		"event_type": "CHECKOUT.ORDER.APPROVED",
		"resource": {"id":"ORD-APP-DUP","status":"APPROVED","purchase_units":[{"custom_id":"7|ORD-DUP","amount":{"currency_code":"USD","value":"50.00"}}]}
	}`)
	header := makeSignatureHeaders("valid_sig")

	ev, err := p.ParseAndVerifyWebhook(context.Background(), header, payload)
	if err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if ev.Status != StatusSucceeded || ev.ProviderTxnID != "CAP-DUP" {
		t.Errorf("unexpected first event: %+v", ev)
	}
	if n := captureCalls.Load(); n != 1 {
		t.Fatalf("expected 1 capture, got %d", n)
	}

	// Replay of the same event id: dedup short-circuits before CaptureOrder.
	if _, err := p.ParseAndVerifyWebhook(context.Background(), header, payload); err != nil {
		t.Fatalf("replayed delivery: %v", err)
	}
	if n := captureCalls.Load(); n != 1 {
		t.Fatalf("replayed webhook must not capture again, got %d captures", n)
	}
	if len(seenOrder) != 1 {
		t.Errorf("expected dedup check to run exactly twice for one id, got %v", seenOrder)
	}
}

func TestPayPalCaptureEventOrderResolution(t *testing.T) {
	build := func(resource string) []byte {
		return []byte(`{"id":"WH-EVT-CAP","event_type":"PAYMENT.CAPTURE.COMPLETED","resource":` + resource + `}`)
	}
	capture := func(t *testing.T, p *PayPal, payload []byte) (WebhookEvent, error) {
		t.Helper()
		return p.ParseAndVerifyWebhook(context.Background(), makeSignatureHeaders("valid_sig"), payload)
	}

	newClient := func() *PayPal {
		var captureCalls atomic.Int32
		srv := paypalWebhookServer(t, &captureCalls)
		t.Cleanup(srv.Close)
		p := NewPayPal("client", "secret", "wh-test")
		p.Base = srv.URL
		return p
	}

	t.Run("resolves via supplementary_data.related_ids", func(t *testing.T) {
		p := newClient()
		payload := build(`{"id":"CAP-REL","status":"COMPLETED","amount":{"currency_code":"USD","value":"10.00"},"supplementary_data":{"related_ids":{"order_id":"9PXL55555A123456B"}}}`)
		ev, err := capture(t, p, payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.OrderNo != "9PXL55555A123456B" {
			t.Errorf("orderNo = %q", ev.OrderNo)
		}
		if ev.ProviderTxnID != "CAP-REL" || ev.AmountCents != 1000 {
			t.Errorf("unexpected event: %+v", ev)
		}
	})

	t.Run("resolves via invoice_id", func(t *testing.T) {
		p := newClient()
		payload := build(`{"id":"CAP-INV","status":"COMPLETED","invoice_id":"ORD-INV-1","amount":{"currency_code":"USD","value":"10.00"}}`)
		ev, err := capture(t, p, payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.OrderNo != "ORD-INV-1" {
			t.Errorf("orderNo = %q", ev.OrderNo)
		}
	})

	t.Run("unresolvable order returns sentinel", func(t *testing.T) {
		p := newClient()
		payload := build(`{"id":"CAP-LOST","status":"COMPLETED","amount":{"currency_code":"USD","value":"10.00"}}`)
		_, err := capture(t, p, payload)
		if !errors.Is(err, ErrPayPalOrderUnresolved) {
			t.Fatalf("expected ErrPayPalOrderUnresolved, got %v", err)
		}
	})
}
