package payment

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCheckoutLimitsRequireCurrencySpecificEvidence(t *testing.T) {
	for _, provider := range []string{ProviderStripe, ProviderWeChat, ProviderAlipay, ProviderPayPal} {
		currency := "CNY"
		if provider == ProviderPayPal {
			currency = "USD"
		}
		m := Merchant{Provider: provider, Currencies: []string{currency}}
		if m.CheckCheckoutAmount(currency, 100) == nil {
			t.Fatalf("%s missing evidence accepted", provider)
		}
		m.CheckoutLimits = map[string]AmountLimit{currency: {MinCents: 100, MaxCents: 10000, Source: "merchant_contract", SettlementCurrency: currency}}
		for _, amount := range []int64{-1, 0, 99, 10001, math.MaxInt64} {
			if m.CheckCheckoutAmount(currency, amount) == nil {
				t.Fatalf("%s accepted %d", provider, amount)
			}
		}
		for _, amount := range []int64{100, 10000} {
			if err := m.CheckCheckoutAmount(currency, amount); err != nil {
				t.Fatalf("%s boundary %d: %v", provider, amount, err)
			}
		}
	}
	m := Merchant{Provider: ProviderStripe, Currencies: []string{"CNY", "USD"}, CheckoutLimits: map[string]AmountLimit{"USD": {MinCents: 50, MaxCents: 99_999_999, SettlementCurrency: "USD", Source: "official"}}}
	if m.CheckCheckoutAmount("USD", 50) != nil || m.CheckCheckoutAmount("USD", 49) == nil || m.CheckCheckoutAmount("CNY", 50) == nil {
		t.Fatal("same settlement currency minimum or isolated currency evidence broken")
	}
	for _, limit := range []AmountLimit{{MinCents: 1, MaxCents: 100, SettlementCurrency: "USD", Source: "official"}, {MinCents: 50, MaxCents: 100, SettlementCurrency: "GBP", Source: "official"}, {MinCents: 50, MaxCents: 100_000_000, SettlementCurrency: "USD", Source: "merchant_contract"}} {
		m.CheckoutLimits["USD"] = limit
		if m.CheckCheckoutAmount("USD", 50) == nil {
			t.Fatalf("invalid evidence accepted %+v", limit)
		}
	}
	m.CheckoutLimits["CNY"] = AmountLimit{MinCents: 300, MaxCents: 10000, SettlementCurrency: "GBP", Source: "merchant_contract"}
	if m.CheckCheckoutAmount("CNY", 300) != nil {
		t.Fatal("explicit cross-settlement contract was rejected despite no automatic conversion")
	}
}

func TestAdapterAmountRejectionMakesNoExternalRequest(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	wx := &WeChatPay{Base: srv.URL, Client: srv.Client()}
	ali := &Alipay{Base: srv.URL, Client: srv.Client()}
	pp := &PayPal{Base: srv.URL, Client: srv.Client()}
	for _, provider := range []Provider{wx, ali, pp} {
		for _, amount := range []int64{-1, 0} {
			currency := "CNY"
			if provider.Name() == ProviderPayPal {
				currency = "USD"
			}
			_, err := provider.CreateCheckout(context.Background(), CheckoutParams{Currency: currency, AmountCents: amount})
			if !errors.Is(err, ErrAmountOutsideLimits) {
				t.Fatalf("%s did not reject amount %d: %v", provider.Name(), amount, err)
			}
		}
	}
	if _, err := ali.CreateCheckout(context.Background(), CheckoutParams{Currency: "CNY", AmountCents: 10_000_000_001}); !errors.Is(err, ErrAmountOutsideLimits) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid amount performed %d external requests", calls.Load())
	}
}

func TestPublishedAPILimitsUseCheckedMinorUnits(t *testing.T) {
	for _, tc := range []struct {
		provider, currency string
		max                int64
	}{{ProviderStripe, "CNY", 99_999_999}, {ProviderStripe, "USD", 99_999_999}, {ProviderAlipay, "CNY", 10_000_000_000}} {
		if ValidateCheckoutAmount(tc.provider, tc.currency, 1) != nil || ValidateCheckoutAmount(tc.provider, tc.currency, tc.max) != nil || !errors.Is(ValidateCheckoutAmount(tc.provider, tc.currency, tc.max+1), ErrAmountOutsideLimits) {
			t.Fatalf("API boundary incorrect %+v", tc)
		}
	}
	for _, tc := range []struct{ provider, currency string }{{ProviderStripe, "JPY"}, {ProviderWeChat, "USD"}, {ProviderAlipay, "USD"}, {ProviderPayPal, "CNY"}} {
		if !errors.Is(ValidateCheckoutAmount(tc.provider, tc.currency, 100), ErrCurrencyMismatch) {
			t.Fatalf("unsupported precision/currency accepted %+v", tc)
		}
	}
}

func TestPlatformSafeMinorAmountIsNotAProviderAllowance(t *testing.T) {
	for _, provider := range []string{ProviderWeChat, ProviderPayPal} {
		currency := "CNY"
		if provider == ProviderPayPal {
			currency = "USD"
		}
		if ValidateCheckoutAmount(provider, currency, MaximumSafeMinorAmount) != nil || !errors.Is(ValidateCheckoutAmount(provider, currency, MaximumSafeMinorAmount+1), ErrAmountOutsideLimits) {
			t.Fatal("platform precision boundary failed")
		}
		m := Merchant{Provider: provider, Currencies: []string{currency}, CheckoutLimits: map[string]AmountLimit{currency: {MinCents: 1, MaxCents: MaximumSafeMinorAmount + 1, SettlementCurrency: currency, Source: "merchant_contract"}}}
		if m.ValidateCheckoutLimits() == nil {
			t.Fatal("unsafe merchant maximum accepted")
		}
		m.CheckoutLimits[currency] = AmountLimit{MinCents: 1, MaxCents: 100, SettlementCurrency: currency, Source: "merchant_contract"}
		if m.CheckCheckoutAmount(currency, MaximumSafeMinorAmount) == nil {
			t.Fatal("platform precision cap was mistaken for merchant allowance")
		}
	}
}
