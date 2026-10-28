package payment

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var recurringIntentID = regexp.MustCompile(`^pi_[A-Za-z0-9_]{1,200}$`)

// StripeRecurringReceipt contains independently retrieved, bounded financial
// facts. Client secrets and instrument details never leave the provider parser.
type StripeRecurringReceipt struct {
	StatusResult
	IdempotencyKey string
	TenantID       int64
	SubscriptionID int64
	LiveMode       bool
}

// QueryRecurringReceipt performs one GET, with no payment creation or capture.
// Unlike an ordinary checkout total, a renewal needs the actual received amount
// and the metadata binding to the server's already committed charge intent.
func (s *Stripe) QueryRecurringReceipt(ctx context.Context, reference string) (StripeRecurringReceipt, error) {
	var result StripeRecurringReceipt
	if !recurringIntentID.MatchString(reference) {
		return result, ErrInvalidPayload
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.Base, "/")+"/v1/payment_intents/"+url.PathEscape(reference), nil)
	if err != nil {
		return result, err
	}
	request.Header.Set("Authorization", "Bearer "+s.SecretKey)
	client := http.Client{Timeout: 20 * time.Second}
	if s.Client != nil {
		client = *s.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return result, errors.New("stripe renewal receipt query unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return result, errors.New("stripe renewal receipt query rejected")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return result, ErrInvalidPayload
	}
	var value struct {
		ID             string            `json:"id"`
		Object         string            `json:"object"`
		Status         string            `json:"status"`
		Amount         int64             `json:"amount"`
		AmountReceived *int64            `json:"amount_received"`
		Currency       string            `json:"currency"`
		LiveMode       *bool             `json:"livemode"`
		Metadata       map[string]string `json:"metadata"`
	}
	if json.Unmarshal(body, &value) != nil || value.ID != reference || value.Object != "payment_intent" || value.LiveMode == nil {
		return result, ErrInvalidPayload
	}
	if value.Status != "succeeded" {
		return result, ErrPaymentNotPaid
	}
	if value.Amount <= 0 || value.AmountReceived == nil || *value.AmountReceived != value.Amount {
		return result, ErrAmountMismatch
	}
	result.Currency = strings.ToUpper(value.Currency)
	if result.Currency != "CNY" && result.Currency != "USD" {
		return result, ErrCurrencyMismatch
	}
	result.TenantID, err = strconv.ParseInt(value.Metadata["tenant_id"], 10, 64)
	if err != nil || result.TenantID <= 0 {
		return result, ErrInvalidPayload
	}
	result.SubscriptionID, err = strconv.ParseInt(value.Metadata["subscription_id"], 10, 64)
	if err != nil || result.SubscriptionID <= 0 {
		return result, ErrInvalidPayload
	}
	result.IdempotencyKey = value.Metadata["idempotency_key"]
	if result.IdempotencyKey == "" || len(result.IdempotencyKey) > 250 || strings.TrimSpace(result.IdempotencyKey) != result.IdempotencyKey {
		return result, ErrInvalidPayload
	}
	result.ProviderTxnID, result.Status, result.AmountCents = reference, StatusSucceeded, value.Amount
	result.LiveMode = *value.LiveMode
	return result, nil
}
