package payment

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestStripeLocalContractSimulation exercises the real HTTP client only against
// an explicitly selected loopback Postman simulation, never a payment provider.
func TestStripeLocalContractSimulation(t *testing.T) {
	endpoint := os.Getenv("URMAIL_PAYMENT_SIMULATION_URL")
	if endpoint == "" {
		t.Skip("URMAIL_PAYMENT_SIMULATION_URL is not set")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		t.Fatal("payment simulation requires an explicit HTTP loopback IP")
	}
	provider := NewStripe("isolated-fixture-only", "unused-fixture-only")
	provider.Base = endpoint
	provider.Client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer provider.Client.CloseIdleConnections()
	result, err := provider.Refund(context.Background(), RefundParams{ProviderTxnID: "pi_isolated", RefundNo: "refund_isolated", AmountCents: 100, Currency: "USD"})
	if os.Getenv("URMAIL_PAYMENT_SIMULATION_FAILURE") != "" {
		if err == nil || !strings.Contains(err.Error(), "stripe refund: status 503") {
			t.Fatalf("expected an actual HTTP 503 from the local provider fixture, got %v", err)
		}
		return
	}
	if err != nil || result.ProviderRefundID != "re_isolated" || result.AmountCents != 100 || result.Currency != "USD" || result.Status != StatusSucceeded || result.ProviderTxnID != "pi_isolated" {
		t.Fatalf("unexpected local fixture response: %+v err=%v", result, err)
	}
}
