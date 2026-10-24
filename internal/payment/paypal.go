package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PayPal implements the Provider interface for PayPal Orders v2 and Payments v2 APIs.
type PayPal struct {
	ClientID     string
	ClientSecret string
	WebhookID    string
	Base         string
	Client       *http.Client

	// EventSeen, when set, is consulted BEFORE any external side effect such
	// as CaptureOrder. It must report whether this webhook event id has
	// already been processed, so a replayed delivery never triggers a second
	// capture. When nil, no caller-side dedup is possible.
	EventSeen func(ctx context.Context, eventID string) (bool, error)

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// Ensure PayPal implements Provider.
var _ Provider = (*PayPal)(nil)

// ErrPayPalOrderUnresolved means the webhook payload carried no field that
// maps to a urMail order; the caller must not guess an order number from the
// raw PayPal object id.
var ErrPayPalOrderUnresolved = errors.New("paypal order reference cannot be resolved from webhook payload")

// resolvePayPalOrderRef picks our order reference from fields that actually
// carry it: custom_id first, then the invoice number, then the PayPal order
// id from supplementary_data.related_ids. It returns a sentinel error when
// nothing resolves.
func resolvePayPalOrderRef(customID, invoiceID, relatedOrderID string) (int64, string, error) {
	orderID, orderNo := parsePayPalReference(customID, "", invoiceID)
	if orderNo == "" && orderID == 0 && relatedOrderID != "" {
		orderNo = relatedOrderID
	}
	if orderNo == "" && orderID == 0 {
		return 0, "", ErrPayPalOrderUnresolved
	}
	return orderID, orderNo, nil
}

// NewPayPal creates a PayPal client targeting the production endpoint by default.
func NewPayPal(clientID, clientSecret, webhookID string) *PayPal {
	return &PayPal{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		WebhookID:    webhookID,
		Base:         "https://api-m.paypal.com",
		Client:       &http.Client{Timeout: 20 * time.Second},
	}
}

// Name returns the provider identifier.
func (p *PayPal) Name() string {
	return ProviderPayPal
}

func (p *PayPal) httpClient() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// getAccessToken fetches or returns a cached OAuth 2.0 bearer access token.
func (p *PayPal) getAccessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	if p.accessToken != "" && time.Now().Before(p.tokenExpiry) {
		token := p.accessToken
		p.mu.Unlock()
		return token, nil
	}
	p.mu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double check under write lock
	if p.accessToken != "" && time.Now().Before(p.tokenExpiry) {
		return p.accessToken, nil
	}

	endpoint := strings.TrimRight(p.Base, "/") + "/v1/oauth2/token"
	data := url.Values{}
	data.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(p.ClientID, p.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("paypal oauth: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("paypal oauth status %d: %s", resp.StatusCode, briefBody(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("paypal oauth unmarshal: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return "", errors.New("paypal oauth: empty access_token received")
	}

	p.accessToken = tokenResp.AccessToken
	expiresIn := tokenResp.ExpiresIn
	if expiresIn > 60 {
		expiresIn -= 60
	}
	p.tokenExpiry = time.Now().Add(time.Duration(expiresIn) * time.Second)

	return p.accessToken, nil
}

// formatPayPalAmount formats cents into a decimal string matching PayPal expectations.
func formatPayPalAmount(amountCents int64, currency string) string {
	upper := strings.ToUpper(currency)
	switch upper {
	case "JPY", "KRW", "VND", "CLP", "PYG", "UGX", "RWF":
		return strconv.FormatInt(amountCents, 10)
	default:
		dollars := amountCents / 100
		cents := amountCents % 100
		if cents < 0 {
			cents = -cents
		}
		return fmt.Sprintf("%d.%02d", dollars, cents)
	}
}

// parsePayPalAmount parses a PayPal decimal string back into amount cents.
// An empty or malformed amount is an error; callers must not ignore it.
func parsePayPalAmount(val string, currency string) (int64, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return 0, errors.New("empty paypal amount")
	}
	upper := strings.ToUpper(currency)
	if upper == "" {
		return 0, fmt.Errorf("missing currency for paypal amount %q", val)
	}
	switch upper {
	case "JPY", "KRW", "VND", "CLP", "PYG", "UGX", "RWF":
		return strconv.ParseInt(val, 10, 64)
	default:
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid paypal amount %q: %w", val, err)
		}
		return int64(math.Round(f * 100)), nil
	}
}

// parsePayPalReference extracts order ID and order number from PayPal identifiers.
func parsePayPalReference(customID, referenceID, invoiceID string) (int64, string) {
	var orderID int64
	var orderNo string

	if customID != "" {
		if strings.Contains(customID, "|") {
			parts := strings.SplitN(customID, "|", 2)
			orderNo = parts[0]
			if id, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				orderID = id
			}
		} else if id, err := strconv.ParseInt(customID, 10, 64); err == nil {
			orderID = id
		} else {
			orderNo = customID
		}
	}

	if orderNo == "" {
		orderNo = referenceID
	}
	if orderNo == "" {
		orderNo = invoiceID
	}

	return orderID, orderNo
}

// PayPalOrder represents a PayPal order object returned by Orders v2 API.
type PayPalOrder struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Intent        string `json:"intent"`
	PurchaseUnits []struct {
		ReferenceID string `json:"reference_id"`
		CustomID    string `json:"custom_id"`
		InvoiceID   string `json:"invoice_id"`
		Amount      struct {
			CurrencyCode string `json:"currency_code"`
			Value        string `json:"value"`
		} `json:"amount"`
		Payments struct {
			Captures []struct {
				ID         string `json:"id"`
				Status     string `json:"status"`
				CreateTime string `json:"create_time"`
				Amount     struct {
					CurrencyCode string `json:"currency_code"`
					Value        string `json:"value"`
				} `json:"amount"`
			} `json:"captures"`
		} `json:"payments"`
	} `json:"purchase_units"`
	Links []struct {
		Href   string `json:"href"`
		Rel    string `json:"rel"`
		Method string `json:"method"`
	} `json:"links"`
}

// CreateOrder initiates a PayPal order with intent CAPTURE.
func (p *PayPal) CreateOrder(ctx context.Context, params CheckoutParams) (PayPalOrder, error) {
	token, err := p.getAccessToken(ctx)
	if err != nil {
		return PayPalOrder{}, fmt.Errorf("paypal auth: %w", err)
	}

	desc := params.Description
	if desc == "" {
		desc = "urMail " + params.OrderNo
	}

	orderReq := map[string]any{
		"intent": "CAPTURE",
		"purchase_units": []map[string]any{
			{
				"reference_id": params.OrderNo,
				"custom_id":    strconv.FormatInt(params.OrderID, 10),
				"invoice_id":   params.OrderNo,
				"description":  desc,
				"amount": map[string]string{
					"currency_code": strings.ToUpper(params.Currency),
					"value":         formatPayPalAmount(params.AmountCents, params.Currency),
				},
			},
		},
		"application_context": map[string]string{
			"return_url":  params.SuccessURL,
			"cancel_url":  params.CancelURL,
			"brand_name":  "urMail",
			"user_action": "PAY_NOW",
		},
	}

	reqBody, err := json.Marshal(orderReq)
	if err != nil {
		return PayPalOrder{}, err
	}

	endpoint := strings.TrimRight(p.Base, "/") + "/v2/checkout/orders"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return PayPalOrder{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if params.OrderNo != "" {
		req.Header.Set("PayPal-Request-Id", "urmail-checkout-"+params.OrderNo)
	}

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return PayPalOrder{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PayPalOrder{}, fmt.Errorf("paypal create order: status %d: %s", resp.StatusCode, briefBody(body))
	}

	var out PayPalOrder
	if err := json.Unmarshal(body, &out); err != nil {
		return PayPalOrder{}, fmt.Errorf("unmarshal create order response: %w", err)
	}
	return out, nil
}

// CreateCheckout initiates a PayPal order and returns the approval link.
func (p *PayPal) CreateCheckout(ctx context.Context, params CheckoutParams) (CheckoutResult, error) {
	order, err := p.CreateOrder(ctx, params)
	if err != nil {
		return CheckoutResult{}, err
	}

	var approveURL string
	for _, l := range order.Links {
		if strings.EqualFold(l.Rel, "approve") || strings.EqualFold(l.Rel, "payer-action") {
			approveURL = l.Href
			break
		}
	}
	if approveURL == "" {
		return CheckoutResult{}, fmt.Errorf("paypal order %s missing approve url", order.ID)
	}

	return CheckoutResult{
		SessionID: order.ID,
		URL:       approveURL,
	}, nil
}

// PayPalCaptureResult holds the outcome of a capture operation.
type PayPalCaptureResult struct {
	OrderID     string
	CaptureID   string
	Status      string
	AmountValue string
	Currency    string
	CreateTime  string
}

// CaptureOrder captures payment for an approved PayPal order.
func (p *PayPal) CaptureOrder(ctx context.Context, orderID string) (PayPalCaptureResult, error) {
	if orderID == "" {
		return PayPalCaptureResult{}, errors.New("orderID required for capture")
	}

	token, err := p.getAccessToken(ctx)
	if err != nil {
		return PayPalCaptureResult{}, fmt.Errorf("paypal auth: %w", err)
	}

	endpoint := strings.TrimRight(p.Base, "/") + "/v2/checkout/orders/" + url.PathEscape(orderID) + "/capture"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return PayPalCaptureResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("PayPal-Request-Id", "urmail-capture-"+orderID)

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return PayPalCaptureResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PayPalCaptureResult{}, fmt.Errorf("paypal capture order: status %d: %s", resp.StatusCode, briefBody(body))
	}

	var res struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		PurchaseUnits []struct {
			ReferenceID string `json:"reference_id"`
			Payments    struct {
				Captures []struct {
					ID         string `json:"id"`
					Status     string `json:"status"`
					CreateTime string `json:"create_time"`
					Amount     struct {
						CurrencyCode string `json:"currency_code"`
						Value        string `json:"value"`
					} `json:"amount"`
				} `json:"captures"`
			} `json:"payments"`
		} `json:"purchase_units"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return PayPalCaptureResult{}, fmt.Errorf("unmarshal capture response: %w", err)
	}

	out := PayPalCaptureResult{
		OrderID: res.ID,
		Status:  res.Status,
	}
	if len(res.PurchaseUnits) > 0 && len(res.PurchaseUnits[0].Payments.Captures) > 0 {
		firstCapture := res.PurchaseUnits[0].Payments.Captures[0]
		out.CaptureID = firstCapture.ID
		if firstCapture.Status != "" {
			out.Status = firstCapture.Status
		}
		out.AmountValue = firstCapture.Amount.Value
		out.Currency = firstCapture.Amount.CurrencyCode
		out.CreateTime = firstCapture.CreateTime
	}
	return out, nil
}

// GetOrder retrieves an order by its ID.
func (p *PayPal) GetOrder(ctx context.Context, orderID string) (PayPalOrder, error) {
	if orderID == "" {
		return PayPalOrder{}, errors.New("orderID required")
	}

	token, err := p.getAccessToken(ctx)
	if err != nil {
		return PayPalOrder{}, fmt.Errorf("paypal auth: %w", err)
	}

	endpoint := strings.TrimRight(p.Base, "/") + "/v2/checkout/orders/" + url.PathEscape(orderID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return PayPalOrder{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return PayPalOrder{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PayPalOrder{}, fmt.Errorf("paypal get order: status %d: %s", resp.StatusCode, briefBody(body))
	}

	var out PayPalOrder
	if err := json.Unmarshal(body, &out); err != nil {
		return PayPalOrder{}, fmt.Errorf("unmarshal paypal order: %w", err)
	}
	return out, nil
}

// QueryPayment checks the payment status of a transaction or order.
func (p *PayPal) QueryPayment(ctx context.Context, _ string, providerTxnID string) (StatusResult, error) {
	if providerTxnID == "" {
		return StatusResult{}, errors.New("provider transaction id required")
	}

	token, err := p.getAccessToken(ctx)
	if err != nil {
		return StatusResult{}, fmt.Errorf("paypal auth: %w", err)
	}

	orderEndpoint := strings.TrimRight(p.Base, "/") + "/v2/checkout/orders/" + url.PathEscape(providerTxnID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, orderEndpoint, nil)
	if err != nil {
		return StatusResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return StatusResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var order PayPalOrder
		if err := json.Unmarshal(body, &order); err != nil {
			return StatusResult{}, fmt.Errorf("unmarshal paypal order: %w", err)
		}

		status := StatusPending
		switch strings.ToUpper(order.Status) {
		case "COMPLETED":
			status = StatusSucceeded
		case "VOIDED":
			status = StatusFailed
		}

		txnID := order.ID
		var amountVal, currency string
		paidAt := time.Now().UTC()

		if len(order.PurchaseUnits) > 0 {
			unit := order.PurchaseUnits[0]
			amountVal = unit.Amount.Value
			currency = unit.Amount.CurrencyCode
			if len(unit.Payments.Captures) > 0 {
				firstCapture := unit.Payments.Captures[0]
				if firstCapture.ID != "" {
					txnID = firstCapture.ID
				}
				if firstCapture.Amount.Value != "" {
					amountVal = firstCapture.Amount.Value
				}
				if firstCapture.Amount.CurrencyCode != "" {
					currency = firstCapture.Amount.CurrencyCode
				}
				if firstCapture.CreateTime != "" {
					if t, err := time.Parse(time.RFC3339, firstCapture.CreateTime); err == nil {
						paidAt = t.UTC()
					}
				}
			}
		}

		amountCents, err := parsePayPalAmount(amountVal, currency)
		if err != nil {
			return StatusResult{}, fmt.Errorf("paypal query order: %w", err)
		}

		return StatusResult{
			ProviderTxnID: txnID,
			Status:        status,
			AmountCents:   amountCents,
			Currency:      strings.ToUpper(currency),
			PaidAt:        paidAt,
		}, nil
	}

	// Fallback: If not found as order, check as capture ID
	if resp.StatusCode == http.StatusNotFound {
		captureEndpoint := strings.TrimRight(p.Base, "/") + "/v2/payments/captures/" + url.PathEscape(providerTxnID)
		cReq, err := http.NewRequestWithContext(ctx, http.MethodGet, captureEndpoint, nil)
		if err != nil {
			return StatusResult{}, err
		}
		cReq.Header.Set("Authorization", "Bearer "+token)
		cReq.Header.Set("Content-Type", "application/json")

		cResp, err := p.httpClient().Do(cReq)
		if err != nil {
			return StatusResult{}, err
		}
		defer func() { _ = cResp.Body.Close() }()

		cBody, _ := io.ReadAll(io.LimitReader(cResp.Body, 1<<20))
		if cResp.StatusCode < 200 || cResp.StatusCode >= 300 {
			return StatusResult{}, fmt.Errorf("paypal query capture: status %d: %s", cResp.StatusCode, briefBody(cBody))
		}

		var capture struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			CreateTime string `json:"create_time"`
			Amount     struct {
				CurrencyCode string `json:"currency_code"`
				Value        string `json:"value"`
			} `json:"amount"`
		}
		if err := json.Unmarshal(cBody, &capture); err != nil {
			return StatusResult{}, fmt.Errorf("unmarshal paypal capture: %w", err)
		}

		status := StatusPending
		switch strings.ToUpper(capture.Status) {
		case "COMPLETED":
			status = StatusSucceeded
		case "REFUNDED":
			status = StatusRefunded
		case "DECLINED", "FAILED":
			status = StatusFailed
		}

		paidAt := time.Now().UTC()
		if capture.CreateTime != "" {
			if t, err := time.Parse(time.RFC3339, capture.CreateTime); err == nil {
				paidAt = t.UTC()
			}
		}

		amountCents, err := parsePayPalAmount(capture.Amount.Value, capture.Amount.CurrencyCode)
		if err != nil {
			return StatusResult{}, fmt.Errorf("paypal query capture: %w", err)
		}

		return StatusResult{
			ProviderTxnID: capture.ID,
			Status:        status,
			AmountCents:   amountCents,
			Currency:      strings.ToUpper(capture.Amount.CurrencyCode),
			PaidAt:        paidAt,
		}, nil
	}

	return StatusResult{}, fmt.Errorf("paypal query: status %d: %s", resp.StatusCode, briefBody(body))
}

// Refund issues a full or partial refund against a capture.
func (p *PayPal) Refund(ctx context.Context, params RefundParams) (RefundResult, error) {
	if params.ProviderTxnID == "" {
		return RefundResult{}, errors.New("provider transaction id required for refund")
	}

	token, err := p.getAccessToken(ctx)
	if err != nil {
		return RefundResult{}, fmt.Errorf("paypal auth: %w", err)
	}

	refundFn := func(targetCaptureID string) (RefundResult, int, error) {
		endpoint := strings.TrimRight(p.Base, "/") + "/v2/payments/captures/" + url.PathEscape(targetCaptureID) + "/refund"

		payload := map[string]any{}
		if params.AmountCents > 0 && params.Currency != "" {
			payload["amount"] = map[string]string{
				"value":         formatPayPalAmount(params.AmountCents, params.Currency),
				"currency_code": strings.ToUpper(params.Currency),
			}
		}
		if params.RefundNo != "" {
			payload["invoice_id"] = params.RefundNo
		}
		if params.Reason != "" {
			payload["note_to_payer"] = params.Reason
		}

		reqBody, err := json.Marshal(payload)
		if err != nil {
			return RefundResult{}, 0, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
		if err != nil {
			return RefundResult{}, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if params.RefundNo != "" {
			req.Header.Set("PayPal-Request-Id", "urmail-refund-"+params.RefundNo)
		}

		resp, err := p.httpClient().Do(req)
		if err != nil {
			return RefundResult{}, 0, err
		}
		defer func() { _ = resp.Body.Close() }()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return RefundResult{}, resp.StatusCode, fmt.Errorf("paypal refund: status %d: %s", resp.StatusCode, briefBody(body))
		}

		var res struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(body, &res); err != nil {
			return RefundResult{}, resp.StatusCode, fmt.Errorf("unmarshal refund response: %w", err)
		}

		var status string
		switch strings.ToUpper(res.Status) {
		case "COMPLETED":
			status = "succeeded"
		case "PENDING":
			status = "processing"
		case "FAILED":
			status = "failed"
		default:
			if strings.HasPrefix(strings.ToUpper(res.Status), "CANCEL") {
				status = "failed"
			} else {
				status = strings.ToLower(res.Status)
			}
		}

		return RefundResult{
			ProviderRefundID: res.ID,
			Status:           status,
		}, resp.StatusCode, nil
	}

	result, code, err := refundFn(params.ProviderTxnID)
	if err == nil {
		return result, nil
	}

	// If capture was not found (404), maybe providerTxnID was an Order ID
	if code == http.StatusNotFound {
		order, getErr := p.GetOrder(ctx, params.ProviderTxnID)
		if getErr == nil && len(order.PurchaseUnits) > 0 && len(order.PurchaseUnits[0].Payments.Captures) > 0 {
			foundCaptureID := order.PurchaseUnits[0].Payments.Captures[0].ID
			if foundCaptureID != "" && foundCaptureID != params.ProviderTxnID {
				r2, _, err2 := refundFn(foundCaptureID)
				if err2 == nil {
					return r2, nil
				}
			}
		}
	}

	return RefundResult{}, err
}

// VerifyWebhookSignature verifies PayPal webhook signature via PayPal's verify-webhook-signature endpoint.
func (p *PayPal) VerifyWebhookSignature(ctx context.Context, header http.Header, body []byte) error {
	if p.WebhookID == "" {
		return fmt.Errorf("%w: paypal webhook id not configured", ErrInvalidSignature)
	}

	getHeader := func(key string) string {
		if v := header.Get(key); v != "" {
			return v
		}
		for k, vs := range header {
			if strings.EqualFold(k, key) && len(vs) > 0 {
				return vs[0]
			}
		}
		return ""
	}

	authAlgo := getHeader("Paypal-Auth-Algo")
	certURL := getHeader("Paypal-Cert-Url")
	transmissionID := getHeader("Paypal-Transmission-Id")
	transmissionSig := getHeader("Paypal-Transmission-Sig")
	transmissionTime := getHeader("Paypal-Transmission-Time")

	if authAlgo == "" || certURL == "" || transmissionID == "" || transmissionSig == "" || transmissionTime == "" {
		return fmt.Errorf("%w: missing required paypal signature headers", ErrInvalidSignature)
	}

	token, err := p.getAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("paypal auth token: %w", err)
	}

	var rawEvent json.RawMessage = body

	verifyPayload := map[string]any{
		"auth_algo":         authAlgo,
		"cert_url":          certURL,
		"transmission_id":   transmissionID,
		"transmission_sig":  transmissionSig,
		"transmission_time": transmissionTime,
		"webhook_id":        p.WebhookID,
		"webhook_event":     rawEvent,
	}

	reqBody, err := json.Marshal(verifyPayload)
	if err != nil {
		return fmt.Errorf("marshal webhook verification payload: %w", err)
	}

	endpoint := strings.TrimRight(p.Base, "/") + "/v1/notifications/verify-webhook-signature"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("verify webhook request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: status %d: %s", ErrInvalidSignature, resp.StatusCode, briefBody(respBody))
	}

	var verifyRes struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := json.Unmarshal(respBody, &verifyRes); err != nil {
		return fmt.Errorf("unmarshal verification response: %w", err)
	}

	if verifyRes.VerificationStatus != "SUCCESS" {
		return fmt.Errorf("%w: verification status %q", ErrInvalidSignature, verifyRes.VerificationStatus)
	}

	return nil
}

// ParseAndVerifyWebhook verifies the signature and parses CHECKOUT.ORDER.APPROVED and PAYMENT.CAPTURE.COMPLETED events.
func (p *PayPal) ParseAndVerifyWebhook(ctx context.Context, header http.Header, body []byte) (WebhookEvent, error) {
	if err := p.VerifyWebhookSignature(ctx, header, body); err != nil {
		return WebhookEvent{}, err
	}

	var envelope struct {
		ID        string          `json:"id"`
		EventType string          `json:"event_type"`
		Resource  json.RawMessage `json:"resource"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return WebhookEvent{}, fmt.Errorf("unmarshal paypal event: %w", err)
	}

	switch envelope.EventType {
	case "CHECKOUT.ORDER.APPROVED":
		var order struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			Intent        string `json:"intent"`
			PurchaseUnits []struct {
				ReferenceID string `json:"reference_id"`
				CustomID    string `json:"custom_id"`
				InvoiceID   string `json:"invoice_id"`
				Amount      struct {
					CurrencyCode string `json:"currency_code"`
					Value        string `json:"value"`
				} `json:"amount"`
				Payments struct {
					Captures []struct {
						ID         string `json:"id"`
						Status     string `json:"status"`
						CreateTime string `json:"create_time"`
						Amount     struct {
							CurrencyCode string `json:"currency_code"`
							Value        string `json:"value"`
						} `json:"amount"`
					} `json:"captures"`
				} `json:"payments"`
			} `json:"purchase_units"`
		}
		if err := json.Unmarshal(envelope.Resource, &order); err != nil {
			return WebhookEvent{}, fmt.Errorf("unmarshal order resource: %w", err)
		}

		var referenceID, customID, invoiceID string
		var amountVal, currency string
		if len(order.PurchaseUnits) > 0 {
			unit := order.PurchaseUnits[0]
			referenceID = unit.ReferenceID
			customID = unit.CustomID
			invoiceID = unit.InvoiceID
			amountVal = unit.Amount.Value
			currency = unit.Amount.CurrencyCode
		}
		orderID, orderNo := parsePayPalReference(customID, referenceID, invoiceID)

		txnID := order.ID
		paidAt := time.Now().UTC()

		// Dedup check MUST run before CaptureOrder: CaptureOrder is an
		// external side effect, and a replayed webhook must not capture twice.
		if p.EventSeen != nil {
			seen, err := p.EventSeen(ctx, envelope.ID)
			if err != nil {
				return WebhookEvent{}, fmt.Errorf("paypal webhook dedup check: %w", err)
			}
			if seen {
				return WebhookEvent{
					Provider:   ProviderPayPal,
					EventID:    envelope.ID,
					EventType:  envelope.EventType,
					OrderID:    orderID,
					OrderNo:    orderNo,
					RawPayload: body,
				}, nil
			}
		}

		switch strings.ToUpper(order.Status) {
		case "APPROVED":
			captureRes, err := p.CaptureOrder(ctx, order.ID)
			if err != nil {
				return WebhookEvent{}, fmt.Errorf("capture paypal order %s: %w", order.ID, err)
			}
			if strings.ToUpper(captureRes.Status) != "COMPLETED" {
				return WebhookEvent{}, fmt.Errorf("%w: paypal capture status is %s", ErrPaymentNotPaid, captureRes.Status)
			}
			if captureRes.CaptureID != "" {
				txnID = captureRes.CaptureID
			}
			if captureRes.AmountValue != "" {
				amountVal = captureRes.AmountValue
			}
			if captureRes.Currency != "" {
				currency = captureRes.Currency
			}
			if captureRes.CreateTime != "" {
				if t, err := time.Parse(time.RFC3339, captureRes.CreateTime); err == nil {
					paidAt = t.UTC()
				}
			}
		case "COMPLETED":
			if len(order.PurchaseUnits) > 0 && len(order.PurchaseUnits[0].Payments.Captures) > 0 {
				firstCapture := order.PurchaseUnits[0].Payments.Captures[0]
				if firstCapture.ID != "" {
					txnID = firstCapture.ID
				}
				if firstCapture.Amount.Value != "" {
					amountVal = firstCapture.Amount.Value
				}
				if firstCapture.Amount.CurrencyCode != "" {
					currency = firstCapture.Amount.CurrencyCode
				}
				if firstCapture.CreateTime != "" {
					if t, err := time.Parse(time.RFC3339, firstCapture.CreateTime); err == nil {
						paidAt = t.UTC()
					}
				}
			}
		default:
			return WebhookEvent{}, fmt.Errorf("%w: paypal order status is %s", ErrPaymentNotPaid, order.Status)
		}

		amountCents, err := parsePayPalAmount(amountVal, currency)
		if err != nil {
			return WebhookEvent{}, fmt.Errorf("parse paypal amount: %w", err)
		}

		return WebhookEvent{
			Provider:      ProviderPayPal,
			EventID:       envelope.ID,
			EventType:     envelope.EventType,
			OrderID:       orderID,
			OrderNo:       orderNo,
			ProviderTxnID: txnID,
			AmountCents:   amountCents,
			Currency:      strings.ToUpper(currency),
			Status:        StatusSucceeded,
			PaidAt:        paidAt,
			RawPayload:    body,
		}, nil

	case "PAYMENT.CAPTURE.COMPLETED":
		var capture struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			CustomID  string `json:"custom_id"`
			InvoiceID string `json:"invoice_id"`
			Amount    struct {
				CurrencyCode string `json:"currency_code"`
				Value        string `json:"value"`
			} `json:"amount"`
			SupplementaryData struct {
				RelatedIDs struct {
					OrderID string `json:"order_id"`
				} `json:"related_ids"`
			} `json:"supplementary_data"`
			CreateTime string `json:"create_time"`
		}
		if err := json.Unmarshal(envelope.Resource, &capture); err != nil {
			return WebhookEvent{}, fmt.Errorf("unmarshal capture resource: %w", err)
		}

		if strings.ToUpper(capture.Status) != "COMPLETED" {
			return WebhookEvent{}, fmt.Errorf("%w: paypal capture status is %s", ErrPaymentNotPaid, capture.Status)
		}

		// A capture object does not reliably carry custom_id / invoice_id;
		// also consult supplementary_data.related_ids before giving up rather
		// than degrading to the raw PayPal order id.
		orderID, orderNo, err := resolvePayPalOrderRef(capture.CustomID, capture.InvoiceID, capture.SupplementaryData.RelatedIDs.OrderID)
		if err != nil {
			return WebhookEvent{}, fmt.Errorf("paypal capture %s: %w", capture.ID, err)
		}

		amountCents, err := parsePayPalAmount(capture.Amount.Value, capture.Amount.CurrencyCode)
		if err != nil {
			return WebhookEvent{}, fmt.Errorf("parse paypal amount: %w", err)
		}

		paidAt := time.Now().UTC()
		if capture.CreateTime != "" {
			if t, err := time.Parse(time.RFC3339, capture.CreateTime); err == nil {
				paidAt = t.UTC()
			}
		}

		return WebhookEvent{
			Provider:      ProviderPayPal,
			EventID:       envelope.ID,
			EventType:     envelope.EventType,
			OrderID:       orderID,
			OrderNo:       orderNo,
			ProviderTxnID: capture.ID,
			AmountCents:   amountCents,
			Currency:      strings.ToUpper(capture.Amount.CurrencyCode),
			Status:        StatusSucceeded,
			PaidAt:        paidAt,
			RawPayload:    body,
		}, nil

	default:
		return WebhookEvent{
			Provider:   ProviderPayPal,
			EventID:    envelope.ID,
			EventType:  envelope.EventType,
			RawPayload: body,
		}, nil
	}
}
