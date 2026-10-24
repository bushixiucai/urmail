package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

func webhookRouter(h *BillingHandler) *gin.Engine {
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, int64(1)) })
	engine.POST("/webhook", h.StripeWebhook)
	return engine
}

// TestStripeWebhookPermanentErrorsAre4xx verifies permanent webhook failures
// are answered 4xx/503 (non-retryable classification), never 500.
func TestStripeWebhookPermanentErrorsAre4xx(t *testing.T) {
	h := NewBillingHandler(store.New(nil))
	h.Pool = &db.Pool{}
	h.Stripe = payment.NewStripe("sk_test", "whsec_test")
	engine := webhookRouter(h)

	// A body signed with the wrong secret is a permanent signature failure.
	body := strings.NewReader(`{"type":"checkout.session.completed"}`)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhook", body)
	req.Header.Set("Stripe-Signature", "t=1,v1=deadbeef")
	res := httptest.NewRecorder()
	engine.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("bad signature: expected 400, got %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), "INVALID_SIGNATURE") {
		t.Fatalf("bad signature: expected INVALID_SIGNATURE, got %s", res.Body.String())
	}

	// A syntactically valid signature over an unparseable payload is a
	// permanent payload failure, also 4xx.
	signed := signTestPayload("whsec_test", `not-json`)
	req = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhook", strings.NewReader(`not-json`))
	req.Header.Set("Stripe-Signature", signed)
	res = httptest.NewRecorder()
	engine.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("bad payload: expected 400, got %d", res.Code)
	}

	// Missing webhook secret is a configuration error and must not look
	// like a retryable server fault either.
	unconfigured := NewBillingHandler(store.New(nil))
	unconfigured.Pool = &db.Pool{}
	unconfigured.Stripe = payment.NewStripe("sk_test", "")
	res = httptest.NewRecorder()
	unconfiguredEngine := webhookRouter(unconfigured)
	unconfiguredEngine.ServeHTTP(res, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhook", strings.NewReader(`{}`)))
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured secret: expected 503, got %d", res.Code)
	}
}

// fakeQuoteStore is a minimal BillingStore fake: only the reads the Quote
// handler performs are implemented; the embedded nil interface panics on
// anything else, which is exactly what we want in a hermetic test.
type fakeQuoteStore struct {
	BillingStore
}

func (f *fakeQuoteStore) GetPlanByCode(_ context.Context, code string) (store.Plans, error) {
	if code == "pro" {
		return store.Plans{Code: code, Name: []byte("{}"), Description: []byte("{}"), PriceCents: 10000, Currency: "CNY", Active: true}, nil
	}
	return store.Plans{}, pgx.ErrNoRows
}

func (f *fakeQuoteStore) GetActiveSubscriptionByTenant(context.Context, int64) (store.Subscriptions, error) {
	return store.Subscriptions{}, pgx.ErrNoRows
}

func (f *fakeQuoteStore) GetCouponRedemptionForOrder(context.Context, store.GetCouponRedemptionForOrderParams) (store.CouponRedemptions, error) {
	return store.CouponRedemptions{}, pgx.ErrNoRows
}

func (f *fakeQuoteStore) GetCouponByCode(_ context.Context, code string) (store.Coupons, error) {
	if code == "SAVE20" {
		return store.Coupons{Code: code, Kind: billing.CouponKindPercent, Value: 20, Active: true}, nil
	}
	return store.Coupons{}, pgx.ErrNoRows
}

// signTestPayload produces a Stripe-Signature header exactly like the
// provider does: HMAC-SHA256 over "t=.payload" with the webhook secret.
func signTestPayload(secret, payload string) string {
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write([]byte(payload))
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

// TestQuoteRequiresSigningKey ensures quote issuance fails closed when no
// HMAC signing key is configured, instead of emitting unsigned quotes.
func TestQuoteRequiresSigningKey(t *testing.T) {
	t.Setenv("URMAIL_QUOTE_HMAC_KEY", "")
	h := NewBillingHandler(&fakeQuoteStore{})
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, int64(1)) })
	engine.POST("/quote", h.Quote)

	res := httptest.NewRecorder()
	engine.ServeHTTP(res, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/quote", strings.NewReader(`{"plan_code":"pro","billing_cycle":"monthly"}`)))
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 without signing key, got %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "QUOTE_KEY_UNCONFIGURED") {
		t.Fatalf("expected QUOTE_KEY_UNCONFIGURED, got %s", res.Body.String())
	}
}

// TestQuoteHonorsCouponCode ensures the coupon_code parameter influences the
// quote instead of being silently ignored.
func TestQuoteHonorsCouponCode(t *testing.T) {
	h := NewBillingHandler(&fakeQuoteStore{})
	h.QuoteHMACKey = []byte("test-signing-key-with-at-least-32-random-bytes")
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, int64(1)) })
	engine.POST("/quote", h.Quote)

	res := httptest.NewRecorder()
	engine.ServeHTTP(res, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/quote", strings.NewReader(
		`{"plan_code":"pro","billing_cycle":"monthly","coupon_code":"SAVE20"}`)))
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "\"discount_cents\":2000") {
		t.Fatalf("expected 20%% discount applied, got %s", res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "\"digest\":") {
		t.Fatalf("quote response must include its signed digest: %s", res.Body.String())
	}

	// An unknown coupon must not pass silently either.
	res = httptest.NewRecorder()
	engine.ServeHTTP(res, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/quote", strings.NewReader(
		`{"plan_code":"pro","billing_cycle":"monthly","coupon_code":"GHOST"}`)))
	if res.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown coupon, got %d %s", res.Code, res.Body.String())
	}
}
