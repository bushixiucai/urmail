// Package payment contains the small Stripe integration used by checkout and
// webhook settlement. It deliberately implements only Checkout Sessions and
// webhook signature verification.
package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Stripe struct {
	SecretKey     string
	WebhookSecret string
	Base          string
	Client        *http.Client
}

func NewStripe(secretKey, webhookSecret string) *Stripe {
	return &Stripe{
		SecretKey: secretKey, WebhookSecret: webhookSecret,
		Base:   "https://api.stripe.com",
		Client: &http.Client{Timeout: 20 * time.Second},
	}
}

type CheckoutSession struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

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
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.Client.Do(req)
	if err != nil {
		return CheckoutSession{}, err
	}
	defer resp.Body.Close()
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

// VerifyWebhook verifies the Stripe-Signature header (t=...,v1=...).
func (s *Stripe) VerifyWebhook(payload []byte, signature string, tolerance time.Duration) error {
	if s.WebhookSecret == "" {
		return fmt.Errorf("stripe webhook secret is not configured")
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
		return fmt.Errorf("invalid stripe signature header")
	}
	if tolerance > 0 && time.Since(time.Unix(timestamp, 0)).Abs() > tolerance {
		return fmt.Errorf("stripe signature outside tolerance")
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
	return fmt.Errorf("stripe signature mismatch")
}

type Event struct {
	Type string `json:"type"`
	Data struct {
		Object struct {
			Metadata struct {
				OrderID string `json:"order_id"`
			} `json:"metadata"`
		} `json:"object"`
	} `json:"data"`
}
