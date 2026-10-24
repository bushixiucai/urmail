package payment

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ParseDecimalAmount enforces exact two-decimal currency units without float arithmetic.
func ParseDecimalAmount(value, currency string) (int64, error) {
	return parsePayPalAmount(value, currency)
}

// ValidateCheckoutDestination confines redirects and native QR payloads to the selected provider.
func ValidateCheckoutDestination(provider string, result CheckoutResult) error {
	if result.SessionID == "" {
		return ErrInvalidPayload
	}
	target := result.URL
	if result.QRCode != "" {
		target = result.QRCode
	}
	u, err := url.Parse(target)
	if err != nil || u.User != nil || u.Fragment != "" {
		return ErrInvalidPayload
	}
	host := strings.ToLower(u.Hostname())
	switch provider {
	case ProviderStripe:
		if u.Scheme != "https" || host != "checkout.stripe.com" {
			return ErrInvalidPayload
		}
	case ProviderPayPal:
		if u.Scheme != "https" || (host != "www.paypal.com" && host != "www.sandbox.paypal.com" && host != "sandbox.paypal.com") {
			return ErrInvalidPayload
		}
	case ProviderAlipay:
		if u.Scheme != "https" || host != "qr.alipay.com" {
			return ErrInvalidPayload
		}
	case ProviderWeChat:
		if u.Scheme != "weixin" || host != "wxpay" || u.Path != "/bizpayurl" || u.Query().Get("pr") == "" {
			return ErrInvalidPayload
		}
	default:
		return ErrUnsupportedOperation
	}
	return nil
}

// ValidateMerchantEvent rechecks environment and recipient binding on already verified callbacks.
func ValidateMerchantEvent(merchant Merchant, raw []byte, event WebhookEvent) error {
	if event.Provider != merchant.Provider {
		return ErrMerchantMismatch
	}
	if merchant.Provider == ProviderStripe {
		var envelope struct {
			Account  string `json:"account"`
			LiveMode *bool  `json:"livemode"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.LiveMode == nil {
			return ErrInvalidPayload
		}
		if *envelope.LiveMode != (merchant.Environment == "live") {
			return ErrMerchantMismatch
		}
		if envelope.Account != "" && envelope.Account != merchant.AccountID {
			return ErrMerchantMismatch
		}
	}
	return nil
}

// VerifyProviderIdentity checks Stripe's stable account using a read-only API.
// Other adapters bind merchant/app/payee identity in their signed transaction protocol.
func VerifyProviderIdentity(ctx context.Context, m Merchant, p Provider) error {
	stripe, ok := p.(*Stripe)
	if !ok {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(stripe.Base, "/")+"/v1/account", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+stripe.SecretKey)
	response, err := stripe.Client.Do(req)
	if err != nil {
		return errors.New("merchant identity verification unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return ErrMerchantMismatch
	}
	var account struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&account) != nil || account.ID != m.AccountID {
		return ErrMerchantMismatch
	}
	return nil
}

// NormalizeRefundStatus keeps pending/unknown provider states from becoming successful refunds.
func NormalizeRefundStatus(value string) string {
	switch strings.ToUpper(value) {
	case "SUCCESS", "SUCCEEDED", "COMPLETED":
		return StatusSucceeded
	case "ABNORMAL":
		return "uncertain"
	case "FAILED", "CLOSED", "CANCELED":
		return StatusFailed
	default:
		if strings.HasPrefix(strings.ToUpper(value), "CANCEL") {
			return StatusFailed
		}
		return "processing"
	}
}
