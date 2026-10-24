package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/store"
)

// MerchantSecrets reuses the existing encrypted secret box; none are returned by configuration APIs.
type MerchantSecrets struct {
	SecretKey            string `json:"secret_key,omitempty"`
	WebhookSecret        string `json:"webhook_secret,omitempty"`
	ClientID             string `json:"client_id,omitempty"`
	ClientSecret         string `json:"client_secret,omitempty"`
	WebhookID            string `json:"webhook_id,omitempty"`
	AppID                string `json:"app_id,omitempty"`
	PrivateKeyPEM        string `json:"private_key_pem,omitempty"`
	ProviderPublicKeyPEM string `json:"provider_public_key_pem,omitempty"`
	ProviderKeyID        string `json:"provider_key_id,omitempty"`
	CertificateSerial    string `json:"certificate_serial,omitempty"`
	APIv3Key             string `json:"api_v3_key,omitempty"`
}

// Merchant identifies one stable account independently of its current signing keys.
type Merchant struct {
	ID                                     int64
	Provider, Environment, AccountID, Name string
	Enabled                                bool
	Currencies                             []string
	Entrypoint                             string
	CheckoutLimits                         map[string]AmountLimit
	Secrets                                MerchantSecrets
}

// Scope identifies the account without using a credential digest.
func (m Merchant) Scope() string { return m.Provider + ":" + m.Environment + ":" + m.AccountID }

// MerchantStore only exposes trusted platform-side configuration reads.
type MerchantStore interface {
	PlatformGetPaymentMerchant(context.Context, int64) (store.PaymentMerchants, error)
	PlatformListPaymentMerchants(context.Context) ([]store.PlatformListPaymentMerchantsRow, error)
	PlatformFindPaymentMerchantByScope(context.Context, store.PlatformFindPaymentMerchantByScopeParams) (store.PaymentMerchants, error)
}

// CredentialBox decrypts existing enc:v1 ciphertext.
type CredentialBox interface{ DecryptString(string) (string, error) }

// Registry constructs provider clients from durable, write-only merchant configuration.
type Registry struct {
	Store   MerchantStore
	Secrets CredentialBox
	BaseURL string
	Client  *http.Client
}

var accountPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,120}$`)

// Validate checks capabilities without external API calls or real payment operations.
func (m Merchant) Validate() error {
	if !accountPattern.MatchString(m.AccountID) || (m.Environment != "live" && m.Environment != "sandbox") || len(strings.TrimSpace(m.Name)) == 0 || len(m.Name) > 200 {
		return errors.New("invalid merchant identity")
	}
	var supported []string
	var entry string
	switch m.Provider {
	case ProviderStripe:
		supported = []string{"CNY", "USD"}
		entry = "hosted"
	case ProviderWeChat:
		supported = []string{"CNY"}
		entry = "native"
		if m.Environment != "live" {
			return errors.New("wechat sandbox transport is not implemented")
		}
	case ProviderAlipay:
		supported = []string{"CNY"}
		entry = "native"
	case ProviderPayPal:
		supported = []string{"USD"}
		entry = "hosted"
	case ProviderAggregated, "tax_invoice":
		if m.Enabled {
			return ErrUnsupportedOperation
		}
		return nil
	default:
		return ErrUnsupportedOperation
	}
	if m.Entrypoint != entry || len(m.Currencies) == 0 {
		return errors.New("merchant capability is not configured")
	}
	seen := map[string]bool{}
	for _, currency := range m.Currencies {
		if !slices.Contains(supported, currency) || seen[currency] {
			return ErrCurrencyMismatch
		}
		seen[currency] = true
	}
	s := m.Secrets
	switch m.Provider {
	case ProviderStripe:
		if s.SecretKey == "" || s.WebhookSecret == "" || strings.ContainsAny(s.SecretKey, " \r\n\t") || !strings.HasPrefix(m.AccountID, "acct_") {
			return errors.New("stripe configuration incomplete")
		}
		if m.Environment == "sandbox" && !strings.HasPrefix(s.SecretKey, "sk_test_") || m.Environment == "live" && !strings.HasPrefix(s.SecretKey, "sk_live_") {
			return errors.New("stripe environment mismatch")
		}
	case ProviderPayPal:
		if s.ClientID == "" || s.ClientSecret == "" || s.WebhookID == "" {
			return errors.New("paypal configuration incomplete")
		}
	case ProviderWeChat, ProviderAlipay:
		private, err := ParseRSAPrivateKeyFromPEM([]byte(s.PrivateKeyPEM))
		if err != nil || private.N.BitLen() < 2048 {
			return errors.New("invalid merchant signing key")
		}
		public, err := ParseRSAPublicKeyFromPEM([]byte(s.ProviderPublicKeyPEM))
		if err != nil || public.N.BitLen() < 2048 {
			return errors.New("invalid provider verification key")
		}
		if !accountPattern.MatchString(s.AppID) {
			return errors.New("merchant app ID missing")
		}
		if m.Provider == ProviderWeChat && (len(s.APIv3Key) != 32 || !accountPattern.MatchString(s.CertificateSerial) || !accountPattern.MatchString(s.ProviderKeyID)) {
			return errors.New("wechat configuration incomplete")
		}
	}
	return nil
}

// Load also supports disabled merchants for verifying already-started payment and refund facts.
func (r *Registry) Load(ctx context.Context, id int64, checkout bool) (Merchant, Provider, error) {
	if r == nil || r.Store == nil || r.Secrets == nil {
		return Merchant{}, nil, ErrUnsupportedOperation
	}
	row, err := r.Store.PlatformGetPaymentMerchant(ctx, id)
	if err != nil {
		return Merchant{}, nil, err
	}
	m := Merchant{ID: row.ID, Provider: row.Provider, Environment: row.Environment, AccountID: row.AccountID, Name: row.Name, Enabled: row.Enabled, Currencies: row.Currencies, Entrypoint: row.Entrypoint}
	if len(row.CheckoutLimits) > 0 && json.Unmarshal(row.CheckoutLimits, &m.CheckoutLimits) != nil {
		return m, nil, errors.New("invalid merchant limits")
	}
	if checkout && !m.Enabled {
		return m, nil, ErrUnsupportedOperation
	}
	if !strings.HasPrefix(row.EncryptedConfig, "enc:v1:") {
		return m, nil, errors.New("merchant encryption is required")
	}
	plain, err := r.Secrets.DecryptString(row.EncryptedConfig)
	if err != nil || json.Unmarshal([]byte(plain), &m.Secrets) != nil {
		return m, nil, errors.New("merchant secrets cannot be read")
	}
	if err = m.Validate(); err != nil {
		return m, nil, err
	}
	provider, err := r.Build(m)
	return m, provider, err
}

// Build binds endpoints to the official provider/environment and pins verification keys.
func (r *Registry) Build(m Merchant) (Provider, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	origin, err := url.Parse(r.BaseURL)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("payment callback origin is invalid")
	}
	callback := strings.TrimRight(r.BaseURL, "/") + fmt.Sprintf("/api/v1/payments/%d/webhook", m.ID)
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	s := m.Secrets
	switch m.Provider {
	case ProviderStripe:
		p := NewStripe(s.SecretKey, s.WebhookSecret)
		p.CheckoutLimits = m.CheckoutLimits
		p.Client = client
		return p, nil
	case ProviderPayPal:
		p := NewPayPal(s.ClientID, s.ClientSecret, s.WebhookID)
		if m.Environment == "sandbox" {
			p.Base = "https://api-m.sandbox.paypal.com"
		}
		p.Client = client
		p.DeferCapture = true
		p.MerchantID = m.AccountID
		return p, nil
	case ProviderAlipay:
		key, _ := ParseRSAPrivateKeyFromPEM([]byte(s.PrivateKeyPEM))
		pub, _ := ParseRSAPublicKeyFromPEM([]byte(s.ProviderPublicKeyPEM))
		p := NewAlipay(s.AppID, key, pub)
		p.SellerID = m.AccountID
		p.NotifyURL = callback
		p.RequireResponseSignature = true
		p.Client = client
		if m.Environment == "sandbox" {
			p.Base = "https://openapi-sandbox.dl.alipaydev.com/gateway.do"
		}
		return p, nil
	case ProviderWeChat:
		key, _ := ParseRSAPrivateKeyFromPEM([]byte(s.PrivateKeyPEM))
		pub, _ := ParseRSAPublicKeyFromPEM([]byte(s.ProviderPublicKeyPEM))
		p := NewWeChatPay(m.AccountID, s.AppID, s.APIv3Key, s.CertificateSerial, key, pub)
		p.NotifyURL = callback
		p.PlatformKeyID = s.ProviderKeyID
		p.RequireResponseSignature = true
		p.Client = client
		return p, nil
	default:
		return nil, ErrUnsupportedOperation
	}
}

// CloseCheckoutProvider reports a provider's actual close capability.
type CloseCheckoutProvider interface {
	CloseCheckout(context.Context, string, string) error
}

// RefundQueryProvider queries an existing refund without initiating another one.
type RefundQueryProvider interface {
	QueryRefund(context.Context, RefundQueryParams) (RefundResult, error)
}

// LoadScope routes historical refunds and queries to their original stable merchant.
func (r *Registry) LoadScope(ctx context.Context, provider, scope string) (Merchant, Provider, error) {
	if r == nil || r.Store == nil {
		return Merchant{}, nil, ErrUnsupportedOperation
	}
	row, err := r.Store.PlatformFindPaymentMerchantByScope(ctx, store.PlatformFindPaymentMerchantByScopeParams{Provider: provider, Provider_2: scope})
	if err != nil {
		return Merchant{}, nil, err
	}
	return r.Load(ctx, row.ID, false)
}

// RefundQueryParams supplies each provider's documented lookup references explicitly.
type RefundQueryParams struct {
	RefundNo, OrderNo, ProviderRefundID, ProviderTxnID, SessionID, ExpectedCurrency string
	ExpectedAmountCents                                                             int64
}
