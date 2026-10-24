// Package payment contains payment provider integrations used by checkout and
// webhook settlement. It implements Checkout Sessions, webhook signature
// verification, status querying, and refunding.
package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Stripe implements the supported Checkout and webhook operations.
type Stripe struct {
	SecretKey     string
	WebhookSecret string
	Base          string
	Client        *http.Client
}

// Ensure Stripe implements Provider interface.
var _ Provider = (*Stripe)(nil)

// Webhook error classification for the Stripe integration. Signature,
// tolerance and payload failures are permanent: the same delivery can never
// succeed later, so the webhook handler must answer 4xx instead of 500 to
// stop provider retries.
var (
	// ErrWebhookNotConfigured means the webhook secret is missing.
	ErrWebhookNotConfigured = errors.New("stripe webhook secret is not configured")
	// ErrInvalidPayload means a signed payload failed to parse.
	ErrInvalidPayload = errors.New("invalid stripe webhook payload")
)

// NewStripe creates a Stripe client with bounded request timeouts.
func NewStripe(secretKey, webhookSecret string) *Stripe {
	return &Stripe{
		SecretKey: secretKey, WebhookSecret: webhookSecret,
		Base:   "https://api.stripe.com",
		Client: &http.Client{Timeout: 20 * time.Second},
	}
}

// Name returns the provider identifier.
func (s *Stripe) Name() string {
	return ProviderStripe
}

// CheckoutSession identifies a hosted checkout destination.
type CheckoutSession struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// CreateCheckoutSession creates a hosted payment session for an order.
func (s *Stripe) CreateCheckoutSession(ctx context.Context, orderID int64, orderNo string, amountCents int64, currency, successURL, cancelURL string) (CheckoutSession, error) {
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", successURL)
	form.Set("cancel_url", cancelURL)
	form.Set("client_reference_id", orderNo)
	form.Set("metadata[order_id]", strconv.FormatInt(orderID, 10))
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", strings.ToLower(currency))
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(amountCents, 10))
	form.Set("line_items[0][price_data][product_data][name]", "urMail "+orderNo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.Base, "/")+"/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return CheckoutSession{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.SecretKey)
	req.Header.Set("Idempotency-Key", "urmail-checkout-"+orderNo)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.Client.Do(req)
	if err != nil {
		return CheckoutSession{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CheckoutSession{}, fmt.Errorf("stripe checkout: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out CheckoutSession
	if err := json.Unmarshal(body, &out); err != nil {
		return CheckoutSession{}, err
	}
	if out.URL == "" {
		return CheckoutSession{}, fmt.Errorf("stripe checkout returned no url")
	}
	return out, nil
}

// CreateCheckout adapts CreateCheckoutSession to the unified Provider interface.
func (s *Stripe) CreateCheckout(ctx context.Context, params CheckoutParams) (CheckoutResult, error) {
	session, err := s.CreateCheckoutSession(ctx, params.OrderID, params.OrderNo, params.AmountCents, params.Currency, params.SuccessURL, params.CancelURL)
	if err != nil {
		return CheckoutResult{}, err
	}
	return CheckoutResult{
		SessionID: session.ID,
		URL:       session.URL,
	}, nil
}

// VerifyWebhook verifies the Stripe-Signature header (t=...,v1=...).
func (s *Stripe) VerifyWebhook(payload []byte, signature string, tolerance time.Duration) error {
	if s.WebhookSecret == "" {
		return ErrWebhookNotConfigured
	}
	var timestamp int64
	var signatures []string
	for _, part := range strings.Split(signature, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			timestamp, _ = strconv.ParseInt(value, 10, 64)
		case "v1":
			signatures = append(signatures, value)
		}
	}
	if timestamp == 0 || len(signatures) == 0 {
		return fmt.Errorf("%w: invalid stripe signature header", ErrInvalidSignature)
	}
	if tolerance > 0 && time.Since(time.Unix(timestamp, 0)).Abs() > tolerance {
		return fmt.Errorf("%w: stripe signature outside tolerance", ErrSignatureExpired)
	}
	mac := hmac.New(sha256.New, []byte(s.WebhookSecret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, signature := range signatures {
		if hmac.Equal([]byte(expected), []byte(signature)) {
			return nil
		}
	}
	return fmt.Errorf("%w: stripe signature mismatch", ErrInvalidSignature)
}

// Event contains the signed webhook fields used for settlement.
type Event struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object struct {
			ID            string `json:"id"`
			PaymentIntent string `json:"payment_intent"`
			PaymentStatus string `json:"payment_status"`
			AmountTotal   int64  `json:"amount_total"`
			// Amount is present on payment_intent objects (not sessions).
			Amount            int64  `json:"amount"`
			Currency          string `json:"currency"`
			ClientReferenceID string `json:"client_reference_id"`
			Metadata          struct {
				OrderID  string `json:"order_id"`
				TenantID string `json:"tenant_id"`
			} `json:"metadata"`
		} `json:"object"`
	} `json:"data"`
}

// ParseAndVerifyWebhook validates the signature and normalizes the Stripe event.
func (s *Stripe) ParseAndVerifyWebhook(_ context.Context, header http.Header, body []byte) (WebhookEvent, error) {
	sig := header.Get("Stripe-Signature")
	if err := s.VerifyWebhook(body, sig, 5*time.Minute); err != nil {
		return WebhookEvent{}, err
	}

	var event Event
	if err := json.Unmarshal(body, &event); err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: unmarshal stripe event: %w", ErrInvalidPayload, err)
	}

	obj := event.Data.Object
	orderID, _ := strconv.ParseInt(obj.Metadata.OrderID, 10, 64)
	txnID := obj.PaymentIntent
	if txnID == "" {
		txnID = obj.ID
	}

	// Settlement-bearing session events. Only an explicit paid (or
	// no_payment_required) status settles; in particular a MISSING
	// payment_status must not be treated as paid.
	switch event.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		if obj.PaymentStatus != "paid" && obj.PaymentStatus != "no_payment_required" {
			return WebhookEvent{}, fmt.Errorf("%w: stripe payment status is %q", ErrPaymentNotPaid, obj.PaymentStatus)
		}
		return WebhookEvent{
			Provider:      ProviderStripe,
			EventID:       event.ID,
			EventType:     event.Type,
			OrderID:       orderID,
			OrderNo:       obj.ClientReferenceID,
			ProviderTxnID: txnID,
			AmountCents:   obj.AmountTotal,
			Currency:      strings.ToUpper(obj.Currency),
			Status:        StatusSucceeded,
			PaidAt:        time.Now().UTC(),
			RawPayload:    body,
		}, nil

	// Terminal non-payment signals: EventType stays populated so the caller
	// can distinguish them from settlement events; the identity and amount
	// fields are still carried so the order can be marked failed.
	case "checkout.session.async_payment_failed", "checkout.session.expired":
		return WebhookEvent{
			Provider:      ProviderStripe,
			EventID:       event.ID,
			EventType:     event.Type,
			OrderID:       orderID,
			OrderNo:       obj.ClientReferenceID,
			ProviderTxnID: txnID,
			AmountCents:   obj.AmountTotal,
			Currency:      strings.ToUpper(obj.Currency),
			Status:        StatusFailed,
			RawPayload:    body,
		}, nil

	// payment_intent.succeeded can arrive without a checkout session event
	// (e.g. after async payment); the order is resolved via the session
	// metadata mirrored onto the payment intent.
	case "payment_intent.succeeded":
		if obj.Metadata.OrderID == "" && obj.ClientReferenceID == "" {
			return WebhookEvent{}, fmt.Errorf("%w: payment_intent.succeeded without order metadata", ErrInvalidPayload)
		}
		amount := obj.AmountTotal
		if amount == 0 {
			amount = obj.Amount
		}
		return WebhookEvent{
			Provider:      ProviderStripe,
			EventID:       event.ID,
			EventType:     event.Type,
			OrderID:       orderID,
			OrderNo:       obj.ClientReferenceID,
			ProviderTxnID: txnID,
			AmountCents:   amount,
			Currency:      strings.ToUpper(obj.Currency),
			Status:        StatusSucceeded,
			PaidAt:        time.Now().UTC(),
			RawPayload:    body,
		}, nil
	}

	// Any other event type is recognised but carries no settlement signal.
	return WebhookEvent{
		Provider:   ProviderStripe,
		EventID:    event.ID,
		EventType:  event.Type,
		RawPayload: body,
	}, nil
}

// QueryPayment queries the status of a Stripe session or payment intent.
func (s *Stripe) QueryPayment(ctx context.Context, _ string, providerTxnID string) (StatusResult, error) {
	if providerTxnID == "" {
		return StatusResult{}, fmt.Errorf("provider transaction id required")
	}

	endpoint := strings.TrimRight(s.Base, "/") + "/v1/checkout/sessions/" + url.PathEscape(providerTxnID)
	if strings.HasPrefix(providerTxnID, "pi_") {
		endpoint = strings.TrimRight(s.Base, "/") + "/v1/payment_intents/" + url.PathEscape(providerTxnID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return StatusResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.SecretKey)
	resp, err := s.Client.Do(req)
	if err != nil {
		return StatusResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return StatusResult{}, fmt.Errorf("stripe query: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var session struct {
		ID            string `json:"id"`
		PaymentStatus string `json:"payment_status"`
		Status        string `json:"status"`
		AmountTotal   int64  `json:"amount_total"`
		Amount        int64  `json:"amount"`
		Currency      string `json:"currency"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		return StatusResult{}, err
	}

	status := StatusPending
	if session.PaymentStatus == "paid" || session.Status == "succeeded" {
		status = StatusSucceeded
	}

	amount := session.AmountTotal
	if amount == 0 {
		amount = session.Amount
	}

	return StatusResult{
		ProviderTxnID: session.ID,
		Status:        status,
		AmountCents:   amount,
		Currency:      strings.ToUpper(session.Currency),
		PaidAt:        time.Now().UTC(),
	}, nil
}

// Refund initiates a refund against a Stripe charge or payment intent.
func (s *Stripe) Refund(ctx context.Context, params RefundParams) (RefundResult, error) {
	if params.ProviderTxnID == "" {
		return RefundResult{}, fmt.Errorf("provider transaction id required for refund")
	}
	// The Idempotency-Key below is derived from RefundNo; an empty RefundNo
	// would make every refund share one key and Stripe would replay the FIRST
	// refund result for 24h instead of issuing the new refund.
	if params.RefundNo == "" {
		return RefundResult{}, fmt.Errorf("refund number required for refund")
	}

	form := url.Values{}
	form.Set("payment_intent", params.ProviderTxnID)
	if params.AmountCents > 0 {
		form.Set("amount", strconv.FormatInt(params.AmountCents, 10))
	}
	form.Set("metadata[refund_no]", params.RefundNo)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.Base, "/")+"/v1/refunds", strings.NewReader(form.Encode()))
	if err != nil {
		return RefundResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.SecretKey)
	req.Header.Set("Idempotency-Key", "urmail-refund-"+params.RefundNo)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.Client.Do(req)
	if err != nil {
		return RefundResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RefundResult{}, fmt.Errorf("stripe refund: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var res struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return RefundResult{}, err
	}

	return RefundResult{
		ProviderRefundID: res.ID,
		Status:           res.Status,
	}, nil
}
