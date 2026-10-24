package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/bushixiucai/urmail/internal/payment"
)

// TestSettleOrderRejectsInvalidAmountFromProvider verifies provider amounts
// must be positive before any database access.
func TestSettleOrderRejectsNegativeAmountFromProvider(t *testing.T) {
	cases := []string{payment.ProviderStripe, payment.ProviderPayPal, payment.ProviderAlipay, payment.ProviderWeChat, "aggregated"}
	for _, provider := range cases {
		// A negative amount is impossible from a real provider.
		err := SettleOrderWithDetails(context.Background(), nil, 42, SettleOptions{
			Provider:    provider,
			AmountCents: -1,
		})
		if !errors.Is(err, ErrPaymentVerificationFailed) {
			t.Errorf("provider %s with negative amount: expected ErrPaymentVerificationFailed, got %v", provider, err)
		}
		// Zero is not a valid provider amount either.
		err = SettleOrderWithDetails(context.Background(), nil, 42, SettleOptions{
			Provider:    provider,
			AmountCents: 0,
		})
		if !errors.Is(err, ErrPaymentVerificationFailed) {
			t.Errorf("provider %s with zero amount: expected ErrPaymentVerificationFailed, got %v", provider, err)
		}
	}

	// Manual settlement remains allowed without a provider-verified amount
	// (it fails later on the nil pool, proving the amount gate passed).
	err := SettleOrderWithDetails(context.Background(), nil, 42, SettleOptions{
		Provider: payment.ProviderManual,
	})
	if err == nil || err.Error() != "database pool is not configured" {
		t.Errorf("manual settlement should proceed past the amount gate, got %v", err)
	}
}
