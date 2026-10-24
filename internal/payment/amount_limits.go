package payment

import (
	"errors"
	"regexp"
	"slices"
)

// AmountLimit records a currency's confirmed checkout limits in integer minor units.
// SettlementCurrency is evidence about the merchant account, never an FX instruction.
type AmountLimit struct {
	MinCents           int64  `json:"min_cents"`
	MaxCents           int64  `json:"max_cents"`
	SettlementCurrency string `json:"settlement_currency"`
	Source             string `json:"limits_source"`
}

// MaximumSafeMinorAmount preserves exact integer JSON amounts across the platform UI.
const MaximumSafeMinorAmount int64 = 9_007_199_254_740_991

// ErrAmountOutsideLimits rejects new payment creation before an external request.
var ErrAmountOutsideLimits = errors.New("payment amount outside confirmed merchant limits")

// ValidateCheckoutAmount checks the supported precision and published API bounds.
// Unknown account-specific ceilings must additionally pass Merchant.CheckoutLimit.
func ValidateCheckoutAmount(provider, currency string, amount int64) error {
	if amount <= 0 || amount > MaximumSafeMinorAmount {
		return ErrAmountOutsideLimits
	}
	switch provider {
	case ProviderStripe:
		// Hosted checkout can offer non-card methods, whose USD/CNY common cap is eight digits.
		if !slices.Contains([]string{"CNY", "USD"}, currency) {
			return ErrCurrencyMismatch
		}
		if amount > 99_999_999 {
			return ErrAmountOutsideLimits
		}
	case ProviderAlipay:
		if currency != "CNY" {
			return ErrCurrencyMismatch
		}
		if amount > 10_000_000_000 {
			return ErrAmountOutsideLimits
		}
	case ProviderWeChat:
		if currency != "CNY" {
			return ErrCurrencyMismatch
		}
	case ProviderPayPal:
		if currency != "USD" {
			return ErrCurrencyMismatch
		}
	default:
		return ErrUnsupportedOperation
	}
	return nil
}

var settlementCurrencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// CheckoutLimit accepts only a documented common rule or explicit merchant-contract evidence.
func (m Merchant) CheckoutLimit(currency string) (AmountLimit, error) {
	limit, ok := m.CheckoutLimits[currency]
	if !ok || !slices.Contains(m.Currencies, currency) || limit.MinCents <= 0 || limit.MaxCents < limit.MinCents {
		return AmountLimit{}, ErrAmountOutsideLimits
	}
	if err := ValidateCheckoutAmount(m.Provider, currency, limit.MaxCents); err != nil {
		return AmountLimit{}, err
	}
	if limit.Source == "official" {
		if m.Provider != ProviderStripe || currency != "USD" || limit.SettlementCurrency != "USD" || limit.MinCents < 50 {
			return AmountLimit{}, ErrAmountOutsideLimits
		}
	} else if limit.Source != "merchant_contract" {
		return AmountLimit{}, ErrAmountOutsideLimits
	}
	if !settlementCurrencyPattern.MatchString(limit.SettlementCurrency) {
		return AmountLimit{}, ErrAmountOutsideLimits
	}
	// Even contract limits cannot override the known same-currency Stripe minimum.
	if m.Provider == ProviderStripe && currency == "USD" && limit.SettlementCurrency == "USD" && limit.MinCents < 50 {
		return AmountLimit{}, ErrAmountOutsideLimits
	}
	return limit, nil
}

// CheckCheckoutAmount applies merchant evidence in addition to the provider API contract.
func (m Merchant) CheckCheckoutAmount(currency string, amount int64) error {
	if err := ValidateCheckoutAmount(m.Provider, currency, amount); err != nil {
		return err
	}
	limit, err := m.CheckoutLimit(currency)
	if err != nil || amount < limit.MinCents || amount > limit.MaxCents {
		return ErrAmountOutsideLimits
	}
	return nil
}

// ValidateCheckoutLimits rejects malformed evidence while allowing an unavailable draft.
func (m Merchant) ValidateCheckoutLimits() error {
	for currency := range m.CheckoutLimits {
		if _, err := m.CheckoutLimit(currency); err != nil {
			return err
		}
	}
	return nil
}

// CheckoutReady reports whether at least one configured currency has confirmed bounds.
func (m Merchant) CheckoutReady() bool {
	for _, currency := range m.Currencies {
		if _, err := m.CheckoutLimit(currency); err == nil {
			return true
		}
	}
	return false
}
