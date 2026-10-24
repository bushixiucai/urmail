package payment

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Alipay implements the Alipay Open Platform payment provider for online checkouts,
// webhook verification with RSA2 signatures, payment queries, and refunds.
type Alipay struct {
	AppID           string
	SellerID        string // required; webhook/query seller_id must match
	PrivateKey      *rsa.PrivateKey
	AlipayPublicKey *rsa.PublicKey
	NotifyURL       string
	ReturnURL       string
	Base            string
	Client          *http.Client
}

// Ensure Alipay implements Provider.
var _ Provider = (*Alipay)(nil)

// NewAlipay creates an Alipay client with default gateway and timeouts.
func NewAlipay(appID string, privateKey *rsa.PrivateKey, alipayPublicKey *rsa.PublicKey) *Alipay {
	return &Alipay{
		AppID:           appID,
		PrivateKey:      privateKey,
		AlipayPublicKey: alipayPublicKey,
		Base:            "https://openapi.alipay.com/gateway.do",
		Client:          &http.Client{Timeout: 20 * time.Second},
	}
}

// Name returns the provider identifier.
func (a *Alipay) Name() string {
	return ProviderAlipay
}

// BuildSignString sorts non-empty parameter map keys in ASCII ascending order
// and concatenates them into "key=value" pairs joined by "&", ignoring "sign" and "sign_type".
func BuildSignString(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(params[k])
	}
	return sb.String()
}

// SignRSASHA256 signs content with an RSA private key using SHA256 (RSA2).
func SignRSASHA256(content string, privateKey *rsa.PrivateKey) (string, error) {
	if privateKey == nil {
		return "", errors.New("private key is nil")
	}
	h := sha256.Sum256([]byte(content))
	sig, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, h[:])
	if err != nil {
		return "", fmt.Errorf("rsa sign: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// VerifyRSASHA256 verifies the RSA2 signature of content using an RSA public key.
func VerifyRSASHA256(content, sigBase64 string, pubKey *rsa.PublicKey) error {
	if pubKey == nil {
		return errors.New("public key is nil")
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sigBase64)
	if err != nil {
		return fmt.Errorf("%w: invalid base64 signature", ErrInvalidSignature)
	}
	h := sha256.Sum256([]byte(content))
	if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, h[:], sigBytes); err != nil {
		return fmt.Errorf("%w: rsa verification failed", ErrInvalidSignature)
	}
	return nil
}

// Sign computes the RSA2 signature for the provided parameter map.
func (a *Alipay) Sign(params map[string]string) (string, error) {
	content := BuildSignString(params)
	return SignRSASHA256(content, a.PrivateKey)
}

// Verify verifies the RSA2 signature for the provided parameter map against AlipayPublicKey.
func (a *Alipay) Verify(params map[string]string, signature string) error {
	content := BuildSignString(params)
	return VerifyRSASHA256(content, signature, a.AlipayPublicKey)
}

// ParseRSAPrivateKeyFromPEM parses a PKCS#1 or PKCS#8 RSA private key from PEM or raw base64.
func ParseRSAPrivateKeyFromPEM(keyData []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(keyData)
	var der []byte
	if block != nil {
		der = block.Bytes
	} else {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyData)))
		if err != nil {
			return nil, fmt.Errorf("failed to decode private key PEM or base64: %w", err)
		}
		der = b
	}

	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	pk8, err := x509.ParsePKCS8PrivateKey(der)
	if err == nil {
		if rsaKey, ok := pk8.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
		return nil, errors.New("pkcs8 key is not RSA private key")
	}
	return nil, fmt.Errorf("failed to parse private key (neither PKCS1 nor PKCS8): %w", err)
}

// ParseRSAPublicKeyFromPEM parses a PKIX or PKCS#1 RSA public key from PEM or raw base64.
func ParseRSAPublicKeyFromPEM(keyData []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(keyData)
	var der []byte
	if block != nil {
		der = block.Bytes
	} else {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyData)))
		if err != nil {
			return nil, fmt.Errorf("failed to decode public key PEM or base64: %w", err)
		}
		der = b
	}

	if pub, err := x509.ParsePKIXPublicKey(der); err == nil {
		if rsaPub, ok := pub.(*rsa.PublicKey); ok {
			return rsaPub, nil
		}
		return nil, errors.New("public key is not RSA")
	}
	if pub, err := x509.ParsePKCS1PublicKey(der); err == nil {
		return pub, nil
	}
	return nil, errors.New("failed to parse public key (neither PKIX nor PKCS1)")
}

func formatCentsToYuan(cents int64) string {
	neg := false
	if cents < 0 {
		neg = true
		cents = -cents
	}
	yuan := cents / 100
	c := cents % 100
	if neg {
		return fmt.Sprintf("-%d.%02d", yuan, c)
	}
	return fmt.Sprintf("%d.%02d", yuan, c)
}

func parseYuanToCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 {
		return 0, fmt.Errorf("invalid decimal format: %s", s)
	}
	yuan, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid yuan amount: %w", err)
	}
	var cents int64
	if len(parts) == 2 {
		dec := parts[1]
		// More than two decimal places would silently truncate real money;
		// reject instead of truncating.
		if len(dec) > 2 {
			return 0, fmt.Errorf("invalid yuan amount %q: at most two decimal places allowed", s)
		}
		switch len(dec) {
		case 0:
			cents = 0
		case 1:
			c, err := strconv.ParseInt(dec, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid cents amount: %w", err)
			}
			cents = c * 10
		case 2:
			c, err := strconv.ParseInt(dec, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid cents amount: %w", err)
			}
			cents = c
		}
	}
	// Overflow-safe yuan*100+cents: the naive expression wraps near MaxInt64.
	if yuan > (math.MaxInt64-cents)/100 {
		return 0, fmt.Errorf("yuan amount %q overflows int64 cents", s)
	}
	total := yuan*100 + cents
	if neg {
		total = -total
	}
	return total, nil
}

func parseAlipayTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().UTC()
	}
	cst := time.FixedZone("CST", 8*3600)
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, cst); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Now().UTC()
}

func (a *Alipay) execute(ctx context.Context, method string, bizContent any) (json.RawMessage, error) {
	if a.PrivateKey == nil {
		return nil, errors.New("alipay merchant private key not configured")
	}

	bizBytes, err := json.Marshal(bizContent)
	if err != nil {
		return nil, fmt.Errorf("marshal bizContent: %w", err)
	}

	params := map[string]string{
		"app_id":      a.AppID,
		"method":      method,
		"format":      "JSON",
		"charset":     "utf-8",
		"sign_type":   "RSA2",
		"timestamp":   time.Now().Format("2006-01-02 15:04:05"),
		"version":     "1.0",
		"biz_content": string(bizBytes),
	}
	if a.NotifyURL != "" {
		params["notify_url"] = a.NotifyURL
	}
	if a.ReturnURL != "" {
		params["return_url"] = a.ReturnURL
	}

	sig, err := a.Sign(params)
	if err != nil {
		return nil, fmt.Errorf("sign request: %w", err)
	}
	params["sign"] = sig

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}

	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Base, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("alipay gateway status %d: %s", resp.StatusCode, briefBody(body))
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("unmarshal alipay response: %w", err)
	}

	responseKey := strings.ReplaceAll(method, ".", "_") + "_response"
	rawResp, ok := root[responseKey]
	if !ok {
		if errResp, hasErr := root["error_response"]; hasErr {
			var errObj struct {
				Code    string `json:"code"`
				Msg     string `json:"msg"`
				SubCode string `json:"sub_code"`
				SubMsg  string `json:"sub_msg"`
			}
			_ = json.Unmarshal(errResp, &errObj)
			return nil, fmt.Errorf("alipay error_response: code %s, msg %s, sub_code %s, sub_msg %s",
				errObj.Code, errObj.Msg, errObj.SubCode, errObj.SubMsg)
		}
		return nil, fmt.Errorf("missing response key %s in alipay response", responseKey)
	}

	var baseResp struct {
		Code    string `json:"code"`
		Msg     string `json:"msg"`
		SubCode string `json:"sub_code"`
		SubMsg  string `json:"sub_msg"`
	}
	if err := json.Unmarshal(rawResp, &baseResp); err != nil {
		return nil, fmt.Errorf("unmarshal base response: %w", err)
	}

	if baseResp.Code != "10000" {
		return nil, fmt.Errorf("alipay API error: code %s, msg %s, sub_code %s, sub_msg %s",
			baseResp.Code, baseResp.Msg, baseResp.SubCode, baseResp.SubMsg)
	}

	return rawResp, nil
}

// CreateCheckout initiates an Alipay precreate QR payment.
func (a *Alipay) CreateCheckout(ctx context.Context, params CheckoutParams) (CheckoutResult, error) {
	subject := params.Description
	if subject == "" {
		subject = "urMail Order " + params.OrderNo
	}

	bizContent := map[string]any{
		"out_trade_no": params.OrderNo,
		"total_amount": formatCentsToYuan(params.AmountCents),
		"subject":      subject,
	}
	if params.OrderID > 0 {
		bizContent["passback_params"] = strconv.FormatInt(params.OrderID, 10)
	}

	rawResp, err := a.execute(ctx, "alipay.trade.precreate", bizContent)
	if err != nil {
		return CheckoutResult{}, err
	}

	var res struct {
		OutTradeNo string `json:"out_trade_no"`
		QRCode     string `json:"qr_code"`
	}
	if err := json.Unmarshal(rawResp, &res); err != nil {
		return CheckoutResult{}, fmt.Errorf("unmarshal precreate response: %w", err)
	}

	if res.QRCode == "" {
		return CheckoutResult{}, errors.New("alipay precreate returned empty qr_code")
	}

	sessionID := res.OutTradeNo
	if sessionID == "" {
		sessionID = params.OrderNo
	}

	return CheckoutResult{
		SessionID: sessionID,
		QRCode:    res.QRCode,
		URL:       res.QRCode,
	}, nil
}

// CreatePagePayURL generates a GET redirect URL for alipay.trade.page.pay.
func (a *Alipay) CreatePagePayURL(params CheckoutParams) (string, error) {
	if a.PrivateKey == nil {
		return "", errors.New("alipay merchant private key not configured")
	}

	subject := params.Description
	if subject == "" {
		subject = "urMail Order " + params.OrderNo
	}

	bizContent := map[string]any{
		"out_trade_no": params.OrderNo,
		"product_code": "FAST_INSTANT_TRADE_PAY",
		"total_amount": formatCentsToYuan(params.AmountCents),
		"subject":      subject,
	}
	if params.OrderID > 0 {
		bizContent["passback_params"] = strconv.FormatInt(params.OrderID, 10)
	}

	bizBytes, err := json.Marshal(bizContent)
	if err != nil {
		return "", fmt.Errorf("marshal biz_content: %w", err)
	}

	queryParams := map[string]string{
		"app_id":      a.AppID,
		"method":      "alipay.trade.page.pay",
		"format":      "JSON",
		"charset":     "utf-8",
		"sign_type":   "RSA2",
		"timestamp":   time.Now().Format("2006-01-02 15:04:05"),
		"version":     "1.0",
		"biz_content": string(bizBytes),
	}
	if a.NotifyURL != "" {
		queryParams["notify_url"] = a.NotifyURL
	}
	returnURL := a.ReturnURL
	if params.SuccessURL != "" {
		returnURL = params.SuccessURL
	}
	if returnURL != "" {
		queryParams["return_url"] = returnURL
	}

	sig, err := a.Sign(queryParams)
	if err != nil {
		return "", fmt.Errorf("sign page pay request: %w", err)
	}
	queryParams["sign"] = sig

	q := url.Values{}
	for k, v := range queryParams {
		q.Set(k, v)
	}

	sep := "?"
	if strings.Contains(a.Base, "?") {
		sep = "&"
	}
	return a.Base + sep + q.Encode(), nil
}

// ParseAndVerifyWebhook validates the Alipay RSA2 signature and normalizes the trade notification.
func (a *Alipay) ParseAndVerifyWebhook(_ context.Context, _ http.Header, body []byte) (WebhookEvent, error) {
	if strings.TrimSpace(a.AppID) == "" || strings.TrimSpace(a.SellerID) == "" {
		return WebhookEvent{}, fmt.Errorf("%w: alipay app_id and seller_id must be configured", ErrMerchantMismatch)
	}
	if a.AlipayPublicKey == nil {
		return WebhookEvent{}, fmt.Errorf("%w: alipay public key not configured", ErrInvalidSignature)
	}

	params := make(map[string]string)
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "{") {
		var jsonMap map[string]any
		if err := json.Unmarshal(body, &jsonMap); err != nil {
			return WebhookEvent{}, fmt.Errorf("unmarshal alipay webhook json: %w", err)
		}
		for k, v := range jsonMap {
			if strVal, ok := v.(string); ok {
				params[k] = strVal
			} else if v != nil {
				params[k] = fmt.Sprint(v)
			}
		}
	} else {
		vals, err := url.ParseQuery(trimmed)
		if err != nil {
			return WebhookEvent{}, fmt.Errorf("parse url-encoded webhook body: %w", err)
		}
		for k, v := range vals {
			if len(v) > 0 {
				params[k] = v[0]
			}
		}
	}

	sig := params["sign"]
	if sig == "" {
		return WebhookEvent{}, fmt.Errorf("%w: missing sign in webhook payload", ErrInvalidSignature)
	}

	if err := a.Verify(params, sig); err != nil {
		return WebhookEvent{}, err
	}

	if a.AppID != "" && params["app_id"] != "" && params["app_id"] != a.AppID {
		return WebhookEvent{}, fmt.Errorf("%w: expected app_id %s, got %s", ErrMerchantMismatch, a.AppID, params["app_id"])
	}

	if params["app_id"] != a.AppID {
		return WebhookEvent{}, fmt.Errorf("%w: expected app_id %s, got %q", ErrMerchantMismatch, a.AppID, params["app_id"])
	}
	if params["seller_id"] != a.SellerID {
		return WebhookEvent{}, fmt.Errorf("%w: expected seller_id %s, got %q", ErrMerchantMismatch, a.SellerID, params["seller_id"])
	}

	tradeStatus := params["trade_status"]
	if tradeStatus != "TRADE_SUCCESS" && tradeStatus != "TRADE_FINISHED" {
		return WebhookEvent{}, fmt.Errorf("%w: trade status is %q", ErrPaymentNotPaid, tradeStatus)
	}

	var orderID int64
	if passback := params["passback_params"]; passback != "" {
		orderID, _ = strconv.ParseInt(passback, 10, 64)
	}
	if orderID == 0 {
		if oid := params["order_id"]; oid != "" {
			orderID, _ = strconv.ParseInt(oid, 10, 64)
		}
	}

	amountCents, err := parseYuanToCents(params["total_amount"])
	if err != nil {
		return WebhookEvent{}, fmt.Errorf("parse total_amount %q: %w", params["total_amount"], err)
	}

	currency := params["currency"]
	if currency == "" {
		currency = "CNY"
	}

	eventID := params["notify_id"]
	if eventID == "" {
		eventID = params["trade_no"]
	}

	eventType := params["notify_type"]
	if eventType == "" {
		eventType = "trade_status_sync"
	}

	paidAtStr := params["gmt_payment"]
	if paidAtStr == "" {
		paidAtStr = params["notify_time"]
	}

	return WebhookEvent{
		Provider:      ProviderAlipay,
		EventID:       eventID,
		EventType:     eventType,
		OrderID:       orderID,
		OrderNo:       params["out_trade_no"],
		ProviderTxnID: params["trade_no"],
		AmountCents:   amountCents,
		Currency:      strings.ToUpper(currency),
		Status:        StatusSucceeded,
		PaidAt:        parseAlipayTime(paidAtStr),
		RawPayload:    body,
	}, nil
}

// QueryPayment queries payment status by orderNo or providerTxnID.
func (a *Alipay) QueryPayment(ctx context.Context, orderNo, providerTxnID string) (StatusResult, error) {
	if strings.TrimSpace(a.AppID) == "" || strings.TrimSpace(a.SellerID) == "" {
		return StatusResult{}, fmt.Errorf("%w: alipay app_id and seller_id must be configured", ErrMerchantMismatch)
	}
	if orderNo == "" && providerTxnID == "" {
		return StatusResult{}, errors.New("orderNo or providerTxnID required")
	}

	bizContent := make(map[string]any)
	if orderNo != "" {
		bizContent["out_trade_no"] = orderNo
	}
	if providerTxnID != "" {
		bizContent["trade_no"] = providerTxnID
	}

	rawResp, err := a.execute(ctx, "alipay.trade.query", bizContent)
	if err != nil {
		return StatusResult{}, err
	}

	var res struct {
		TradeNo     string `json:"trade_no"`
		OutTradeNo  string `json:"out_trade_no"`
		TradeStatus string `json:"trade_status"`
		TotalAmount string `json:"total_amount"`
		SendPayDate string `json:"send_pay_date"`
		SellerID    string `json:"seller_id"`
	}
	if err := json.Unmarshal(rawResp, &res); err != nil {
		return StatusResult{}, fmt.Errorf("unmarshal query response: %w", err)
	}

	if res.SellerID != a.SellerID || res.OutTradeNo != orderNo && orderNo != "" {
		return StatusResult{}, fmt.Errorf("%w: seller/order identity does not match query", ErrMerchantMismatch)
	}

	status := StatusPending
	switch res.TradeStatus {
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		status = StatusSucceeded
	case "TRADE_CLOSED":
		status = StatusFailed
	case "WAIT_BUYER_PAY":
		status = StatusPending
	}

	amountCents, err := parseYuanToCents(res.TotalAmount)
	if err != nil {
		return StatusResult{}, fmt.Errorf("parse total_amount %q: %w", res.TotalAmount, err)
	}

	txnID := res.TradeNo
	if txnID == "" {
		txnID = providerTxnID
	}

	return StatusResult{
		ProviderTxnID: txnID,
		Status:        status,
		AmountCents:   amountCents,
		Currency:      "CNY",
		PaidAt:        parseAlipayTime(res.SendPayDate),
	}, nil
}

// Refund initiates an Alipay refund.
func (a *Alipay) Refund(ctx context.Context, params RefundParams) (RefundResult, error) {
	if params.OrderNo == "" && params.ProviderTxnID == "" {
		return RefundResult{}, errors.New("orderNo or providerTxnID required for refund")
	}
	if params.AmountCents <= 0 {
		return RefundResult{}, fmt.Errorf("refund amount must be positive, got %d", params.AmountCents)
	}
	if params.OriginalAmountCents > 0 && params.AmountCents > params.OriginalAmountCents {
		return RefundResult{}, fmt.Errorf("refund amount %d exceeds original total %d", params.AmountCents, params.OriginalAmountCents)
	}

	bizContent := map[string]any{
		"refund_amount": formatCentsToYuan(params.AmountCents),
	}
	if params.OrderNo != "" {
		bizContent["out_trade_no"] = params.OrderNo
	}
	if params.ProviderTxnID != "" {
		bizContent["trade_no"] = params.ProviderTxnID
	}
	if params.RefundNo != "" {
		bizContent["out_request_no"] = params.RefundNo
	}
	if params.Reason != "" {
		bizContent["refund_reason"] = params.Reason
	}

	rawResp, err := a.execute(ctx, "alipay.trade.refund", bizContent)
	if err != nil {
		return RefundResult{}, err
	}

	var res struct {
		TradeNo      string `json:"trade_no"`
		OutTradeNo   string `json:"out_trade_no"`
		OutRequestNo string `json:"out_request_no"`
		FundChange   string `json:"fund_change"`
		RefundFee    string `json:"refund_fee"`
	}
	if err := json.Unmarshal(rawResp, &res); err != nil {
		return RefundResult{}, fmt.Errorf("unmarshal refund response: %w", err)
	}

	refundID := res.TradeNo
	if refundID == "" {
		refundID = res.OutRequestNo
	}
	if refundID == "" {
		refundID = params.RefundNo
	}

	// fund_change == "Y" is Alipay's signal that funds actually moved.
	// Anything else must not be reported as succeeded.
	if res.FundChange != "Y" {
		return RefundResult{
			ProviderRefundID: refundID,
			Status:           StatusPending,
		}, nil
	}

	return RefundResult{
		ProviderRefundID: refundID,
		Status:           StatusSucceeded,
	}, nil
}
