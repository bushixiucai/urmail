package payment

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Supported payment provider names.
const (
	ProviderStripe     = "stripe"
	ProviderWeChat     = "wechat"
	ProviderAlipay     = "alipay"
	ProviderPayPal     = "paypal"
	ProviderAggregated = "aggregated"
	ProviderManual     = "manual"
)

// Standard payment statuses matching payments.status and orders.status.
const (
	StatusPending   = "pending"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusRefunded  = "refunded"
)

// Common payment domain errors.
var (
	ErrInvalidSignature     = errors.New("invalid webhook signature")
	ErrSignatureExpired     = errors.New("webhook signature expired")
	ErrPaymentNotPaid       = errors.New("payment is not completed/paid")
	ErrAmountMismatch       = errors.New("payment amount mismatch")
	ErrCurrencyMismatch     = errors.New("payment currency mismatch")
	ErrMerchantMismatch     = errors.New("payment merchant mismatch")
	ErrUnsupportedOperation = errors.New("operation not supported by provider")
)

// CheckoutParams contains parameters needed to initiate a hosted or QR checkout.
type CheckoutParams struct {
	TenantID    int64
	OrderID     int64
	OrderNo     string
	AmountCents int64
	Currency    string
	Description string
	ClientIP    string
	SuccessURL  string
	CancelURL   string
}

// CheckoutResult contains the response from initiating a checkout session.
type CheckoutResult struct {
	SessionID string `json:"session_id"`
	URL       string `json:"url,omitempty"`
	QRCode    string `json:"qr_code,omitempty"`
}

// WebhookEvent represents a verified, normalized event emitted by a payment gateway.
type WebhookEvent struct {
	Provider      string    `json:"provider"`
	EventID       string    `json:"event_id"`
	EventType     string    `json:"event_type"`
	OrderID       int64     `json:"order_id"`
	OrderNo       string    `json:"order_no"`
	ProviderTxnID string    `json:"provider_txn_id"`
	AmountCents   int64     `json:"amount_cents"`
	Currency      string    `json:"currency"`
	Status        string    `json:"status"` // StatusSucceeded, StatusFailed, StatusRefunded
	PaidAt        time.Time `json:"paid_at"`
	RawPayload    []byte    `json:"-"`
}

// StatusResult represents the query result of an external transaction.
type StatusResult struct {
	ProviderTxnID string    `json:"provider_txn_id"`
	Status        string    `json:"status"`
	AmountCents   int64     `json:"amount_cents"`
	Currency      string    `json:"currency"`
	PaidAt        time.Time `json:"paid_at,omitempty"`
}

// RefundParams contains fields needed to initiate a refund.
type RefundParams struct {
	TenantID      int64
	OrderID       int64
	OrderNo       string
	ProviderTxnID string
	RefundNo      string
	AmountCents   int64
	// OriginalAmountCents is the original order total. Providers such as
	// WeChat Pay require it in amount.total alongside amount.refund.
	// When zero, AmountCents is used as a compatibility fallback.
	OriginalAmountCents int64
	Currency            string
	Reason              string
}

// maxUpstreamBodyInErr bounds how much of an upstream response body is
// embedded in returned error messages (never include auth headers).
const maxUpstreamBodyInErr = 256

// briefBody renders an upstream response body safe for inclusion in an
// error message: whitespace-collapsed and truncated.
func briefBody(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	if len(s) > maxUpstreamBodyInErr {
		s = s[:maxUpstreamBodyInErr] + "...(truncated)"
	}
	return s
}

// RefundResult contains the result of a refund request.
type RefundResult struct {
	ProviderRefundID string `json:"provider_refund_id"`
	Status           string `json:"status"` // processing, succeeded, failed
}

// Provider defines the interface that all payment adapters must implement.
type Provider interface {
	Name() string
	CreateCheckout(ctx context.Context, params CheckoutParams) (CheckoutResult, error)
	ParseAndVerifyWebhook(ctx context.Context, header http.Header, body []byte) (WebhookEvent, error)
	QueryPayment(ctx context.Context, orderNo, providerTxnID string) (StatusResult, error)
	Refund(ctx context.Context, params RefundParams) (RefundResult, error)
}
