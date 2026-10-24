package payment

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// TestStripeWebhookErrorClassification pins the sentinel classification of
// webhook failures so the HTTP layer can answer permanent errors with 4xx
// instead of a retryable 500, via errors.Is rather than string matching.
func TestStripeWebhookErrorClassification(t *testing.T) {
	s := NewStripe("sk", "whsec")
	now := time.Now().Unix()

	t.Run("bad signature is ErrInvalidSignature", func(t *testing.T) {
		err := s.VerifyWebhook([]byte("{}"), "t="+strconv.FormatInt(now, 10)+",v1=deadbeef", 5*time.Minute)
		if !errors.Is(err, ErrInvalidSignature) {
			t.Errorf("expected ErrInvalidSignature, got %v", err)
		}
	})

	t.Run("expired signature is ErrSignatureExpired", func(t *testing.T) {
		err := s.VerifyWebhook([]byte("{}"), sign("whsec", now-3600, []byte("{}")), 5*time.Minute)
		if !errors.Is(err, ErrSignatureExpired) {
			t.Errorf("expected ErrSignatureExpired, got %v", err)
		}
	})

	t.Run("malformed signature header is ErrInvalidSignature", func(t *testing.T) {
		err := s.VerifyWebhook([]byte("{}"), "garbage", 5*time.Minute)
		if !errors.Is(err, ErrInvalidSignature) {
			t.Errorf("expected ErrInvalidSignature, got %v", err)
		}
	})

	t.Run("missing secret is ErrWebhookNotConfigured", func(t *testing.T) {
		err := NewStripe("sk", "").VerifyWebhook([]byte("{}"), "t=1,v1=x", time.Minute)
		if !errors.Is(err, ErrWebhookNotConfigured) {
			t.Errorf("expected ErrWebhookNotConfigured, got %v", err)
		}
	})

	t.Run("signed but unparseable payload is ErrInvalidPayload", func(t *testing.T) {
		header := http.Header{}
		header.Set("Stripe-Signature", sign("whsec", now, []byte("not-json")))
		_, err := s.ParseAndVerifyWebhook(context.Background(), header, []byte("not-json"))
		if !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("expected ErrInvalidPayload, got %v", err)
		}
	})

	t.Run("unpaid session remains ErrPaymentNotPaid", func(t *testing.T) {
		payload := []byte(`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"payment_status":"unpaid"}}}`)
		header := http.Header{}
		header.Set("Stripe-Signature", sign("whsec", now, payload))
		_, err := s.ParseAndVerifyWebhook(context.Background(), header, payload)
		if !errors.Is(err, ErrPaymentNotPaid) {
			t.Errorf("expected ErrPaymentNotPaid, got %v", err)
		}
	})
}
