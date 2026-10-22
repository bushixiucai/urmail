package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
		if r.PostForm.Get("metadata[order_id]") != "42" {
			t.Errorf("order metadata = %q", r.PostForm.Get("metadata[order_id]"))
		}
		_, _ = w.Write([]byte(`{"id":"cs_1","url":"https://checkout.stripe.test/cs_1"}`))
	}))
	defer srv.Close()
	s := NewStripe("sk", "whsec")
	s.Base = srv.URL
	got, err := s.CreateCheckoutSession(context.Background(), 42, "ORD-1", 1000, "cny", "https://a", "https://b")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://checkout.stripe.test/cs_1" {
		t.Fatalf("url = %s", got.URL)
	}
}
