package payment

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func recurringReceiptFixture() map[string]any {
	return map[string]any{"id": "pi_renewal_one", "object": "payment_intent", "status": "succeeded", "amount": 1200, "amount_received": 1200, "currency": "cny", "livemode": false,
		"client_secret": "must-never-leave-the-parser", "metadata": map[string]string{"idempotency_key": "41:20261028T120000Z:27:charge-1", "tenant_id": "11", "subscription_id": "41"}}
}

func TestStripeRecurringReceiptRequiresActualFundsAndServerBinding(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		pass bool
	}{
		{"confirmed", func(map[string]any) {}, true},
		{"missing actual funds", func(v map[string]any) { delete(v, "amount_received") }, false},
		{"partial funds", func(v map[string]any) { v["amount_received"] = 1199 }, false},
		{"not paid", func(v map[string]any) { v["status"] = "processing" }, false},
		{"wrong transaction", func(v map[string]any) { v["id"] = "pi_other" }, false},
		{"missing environment", func(v map[string]any) { delete(v, "livemode") }, false},
		{"missing binding", func(v map[string]any) { v["metadata"] = map[string]string{} }, false},
		{"unsupported currency", func(v map[string]any) { v["currency"] = "jpy" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := recurringReceiptFixture()
			tc.edit(value)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/payment_intents/pi_renewal_one" || r.Header.Get("Authorization") != "Bearer synthetic-test-key" {
					t.Error("receipt reconciliation changed request method or identity")
				}
				_ = json.NewEncoder(w).Encode(value)
			}))
			defer server.Close()
			stripe := NewStripe("synthetic-test-key", "")
			stripe.Base = server.URL
			got, err := stripe.QueryRecurringReceipt(t.Context(), "pi_renewal_one")
			if (err == nil) != tc.pass {
				t.Fatalf("receipt accepted=%t want=%t: %v", err == nil, tc.pass, err)
			}
			if tc.pass && (got.AmountCents != 1200 || got.TenantID != 11 || got.SubscriptionID != 41 || got.Currency != "CNY" || got.LiveMode || got.Status != StatusSucceeded) {
				t.Fatal("receipt lost a verified financial binding")
			}
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), "must-never-leave-the-parser") {
				t.Fatal("provider client secret escaped parsed receipt")
			}
		})
	}
}

func TestStripeRecurringReceiptRefusesRedirectAndLargeResponses(t *testing.T) {
	var targetHit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetHit.Store(true) }))
	defer target.Close()
	for _, large := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if large {
				_, _ = fmt.Fprint(w, strings.Repeat("x", (1<<20)+1))
				return
			}
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		}))
		stripe := NewStripe("synthetic-test-key", "")
		stripe.Base = server.URL
		_, err := stripe.QueryRecurringReceipt(t.Context(), "pi_renewal_one")
		server.Close()
		if err == nil || targetHit.Load() {
			t.Fatal("unbounded or redirected financial receipt was accepted")
		}
	}
}
