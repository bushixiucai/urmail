package payment

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// WeChatPay implements the WeChat Pay APIv3 client for native QR payments,
// webhook verification with AEAD_AES_256_GCM decryption, query and refunds.
type WeChatPay struct {
	MchID          string
	AppID          string
	APIv3Key       []byte // 32 bytes AES-256 key
	CertSerialNo   string // Merchant cert serial
	PrivateKey     *rsa.PrivateKey
	PlatformPubKey *rsa.PublicKey
	Base           string
	Client         *http.Client
}

// Ensure WeChatPay implements Provider.
var _ Provider = (*WeChatPay)(nil)

// NewWeChatPay creates a WeChat Pay v3 client with bounded timeouts.
func NewWeChatPay(mchID, appID string, apiv3Key string, certSerialNo string, privateKey *rsa.PrivateKey, platformPubKey *rsa.PublicKey) *WeChatPay {
	return &WeChatPay{
		MchID:          mchID,
		AppID:          appID,
		APIv3Key:       []byte(apiv3Key),
		CertSerialNo:   certSerialNo,
		PrivateKey:     privateKey,
		PlatformPubKey: platformPubKey,
		Base:           "https://api.mch.weixin.qq.com",
		Client:         &http.Client{Timeout: 20 * time.Second},
	}
}

// Name returns the provider identifier.
func (w *WeChatPay) Name() string {
	return ProviderWeChat
}

// buildAuthHeader generates the WeChatPay v3 HTTP Authorization header.
func (w *WeChatPay) buildAuthHeader(method, path string, body []byte) (string, error) {
	if w.PrivateKey == nil {
		return "", errors.New("wechat merchant private key not configured")
	}

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	nonceStr := base64.RawURLEncoding.EncodeToString(nonce)

	message := method + "\n" + path + "\n" + ts + "\n" + nonceStr + "\n" + string(body) + "\n"
	hashed := sha256.Sum256([]byte(message))
	sig, err := rsa.SignPKCS1v15(rand.Reader, w.PrivateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("sign wechat request: %w", err)
	}
	sigBase64 := base64.StdEncoding.EncodeToString(sig)

	auth := fmt.Sprintf("WECHATPAY2-SHA256-RSA2048 mchid=%q,nonce_str=%q,signature=%q,timestamp=%q,serial_no=%q",
		w.MchID, nonceStr, sigBase64, ts, w.CertSerialNo)
	return auth, nil
}

// CreateCheckout initiates a native WeChat Pay QR order.
func (w *WeChatPay) CreateCheckout(ctx context.Context, params CheckoutParams) (CheckoutResult, error) {
	path := "/v3/pay/transactions/native"
	reqBody := map[string]any{
		"mchid":        w.MchID,
		"appid":        w.AppID,
		"description":  params.Description,
		"out_trade_no": params.OrderNo,
		"attach":       strconv.FormatInt(params.OrderID, 10),
		"amount": map[string]any{
			"total":    params.AmountCents,
			"currency": strings.ToUpper(params.Currency),
		},
	}
	if reqBody["description"] == "" {
		reqBody["description"] = "urMail Order " + params.OrderNo
	}

	rawReq, err := json.Marshal(reqBody)
	if err != nil {
		return CheckoutResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(w.Base, "/")+path, bytes.NewReader(rawReq))
	if err != nil {
		return CheckoutResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	auth, err := w.buildAuthHeader(http.MethodPost, path, rawReq)
	if err != nil {
		return CheckoutResult{}, err
	}
	req.Header.Set("Authorization", auth)

	resp, err := w.Client.Do(req)
	if err != nil {
		return CheckoutResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CheckoutResult{}, fmt.Errorf("wechat native checkout: status %d: %s", resp.StatusCode, briefBody(body))
	}

	var res struct {
		CodeURL string `json:"code_url"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return CheckoutResult{}, err
	}

	return CheckoutResult{
		SessionID: params.OrderNo,
		QRCode:    res.CodeURL,
		URL:       res.CodeURL,
	}, nil
}

// DecryptNotification decrypts the AEAD_AES_256_GCM resource payload in a WeChat callback.
func (w *WeChatPay) DecryptNotification(ciphertext, nonce, associatedData string) ([]byte, error) {
	if len(w.APIv3Key) != 32 {
		return nil, errors.New("wechat apiv3 key must be 32 bytes")
	}

	cipherData, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}

	block, err := aes.NewCipher(w.APIv3Key)
	if err != nil {
		return nil, err
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	plain, err := aesGCM.Open(nil, []byte(nonce), cipherData, []byte(associatedData))
	if err != nil {
		return nil, fmt.Errorf("aes gcm decrypt: %w", err)
	}
	return plain, nil
}

// ParseAndVerifyWebhook verifies WeChat Pay signature and decrypts the transaction payload.
func (w *WeChatPay) ParseAndVerifyWebhook(_ context.Context, header http.Header, body []byte) (WebhookEvent, error) {
	ts := header.Get("Wechatpay-Timestamp")
	nonce := header.Get("Wechatpay-Nonce")
	sig := header.Get("Wechatpay-Signature")

	if ts == "" || nonce == "" || sig == "" {
		return WebhookEvent{}, fmt.Errorf("%w: missing required wechat signature headers", ErrInvalidSignature)
	}

	// Verify timestamp freshness within 5 minutes
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(tsInt, 0)).Abs() > 5*time.Minute {
		return WebhookEvent{}, ErrSignatureExpired
	}

	// Verify RSA signature; verification is mandatory (fail closed).
	if w.PlatformPubKey == nil {
		return WebhookEvent{}, fmt.Errorf("%w: wechat platform public key not configured", ErrInvalidSignature)
	}
	{
		sigBytes, err := base64.StdEncoding.DecodeString(sig)
		if err != nil {
			return WebhookEvent{}, fmt.Errorf("%w: bad signature base64", ErrInvalidSignature)
		}
		message := ts + "\n" + nonce + "\n" + string(body) + "\n"
		hashed := sha256.Sum256([]byte(message))
		if err := rsa.VerifyPKCS1v15(w.PlatformPubKey, crypto.SHA256, hashed[:], sigBytes); err != nil {
			return WebhookEvent{}, fmt.Errorf("%w: rsa verify failed", ErrInvalidSignature)
		}
	}

	var notification struct {
		ID           string `json:"id"`
		EventType    string `json:"event_type"`
		ResourceType string `json:"resource_type"`
		Resource     struct {
			Algorithm      string `json:"algorithm"`
			Ciphertext     string `json:"ciphertext"`
			AssociatedData string `json:"associated_data"`
			Nonce          string `json:"nonce"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(body, &notification); err != nil {
		return WebhookEvent{}, fmt.Errorf("unmarshal wechat notification: %w", err)
	}

	if notification.EventType != "TRANSACTION.SUCCESS" {
		return WebhookEvent{
			Provider:   ProviderWeChat,
			EventID:    notification.ID,
			EventType:  notification.EventType,
			RawPayload: body,
		}, nil
	}

	plain, err := w.DecryptNotification(notification.Resource.Ciphertext, notification.Resource.Nonce, notification.Resource.AssociatedData)
	if err != nil {
		return WebhookEvent{}, fmt.Errorf("decrypt wechat resource: %w", err)
	}

	var txn struct {
		MchID         string `json:"mchid"`
		AppID         string `json:"appid"`
		OutTradeNo    string `json:"out_trade_no"`
		TransactionID string `json:"transaction_id"`
		TradeState    string `json:"trade_state"`
		SuccessTime   string `json:"success_time"`
		Attach        string `json:"attach"`
		Amount        struct {
			Total    int64  `json:"total"`
			Currency string `json:"currency"`
		} `json:"amount"`
	}
	if err := json.Unmarshal(plain, &txn); err != nil {
		return WebhookEvent{}, fmt.Errorf("unmarshal decrypted txn: %w", err)
	}

	if w.MchID == "" || txn.MchID == "" || txn.MchID != w.MchID {
		return WebhookEvent{}, fmt.Errorf("%w: configured mchid %q, callback mchid %q", ErrMerchantMismatch, w.MchID, txn.MchID)
	}

	if txn.TradeState != "SUCCESS" {
		return WebhookEvent{}, fmt.Errorf("%w: trade state is %s", ErrPaymentNotPaid, txn.TradeState)
	}

	orderID, _ := strconv.ParseInt(txn.Attach, 10, 64)
	paidAt := time.Now().UTC()
	if t, err := time.Parse(time.RFC3339, txn.SuccessTime); err == nil {
		paidAt = t.UTC()
	}

	currency := txn.Amount.Currency
	if currency == "" {
		currency = "CNY"
	}

	return WebhookEvent{
		Provider:      ProviderWeChat,
		EventID:       notification.ID,
		EventType:     notification.EventType,
		OrderID:       orderID,
		OrderNo:       txn.OutTradeNo,
		ProviderTxnID: txn.TransactionID,
		AmountCents:   txn.Amount.Total,
		Currency:      strings.ToUpper(currency),
		Status:        StatusSucceeded,
		PaidAt:        paidAt,
		RawPayload:    body,
	}, nil
}

// QueryPayment queries payment status by out_trade_no.
func (w *WeChatPay) QueryPayment(ctx context.Context, orderNo, _ string) (StatusResult, error) {
	if orderNo == "" {
		return StatusResult{}, errors.New("orderNo required")
	}

	path := fmt.Sprintf("/v3/pay/transactions/out-trade-no/%s?mchid=%s", url.PathEscape(orderNo), url.QueryEscape(w.MchID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(w.Base, "/")+path, nil)
	if err != nil {
		return StatusResult{}, err
	}
	req.Header.Set("Accept", "application/json")
	auth, err := w.buildAuthHeader(http.MethodGet, path, nil)
	if err != nil {
		return StatusResult{}, err
	}
	req.Header.Set("Authorization", auth)

	resp, err := w.Client.Do(req)
	if err != nil {
		return StatusResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return StatusResult{}, fmt.Errorf("wechat query: status %d: %s", resp.StatusCode, briefBody(body))
	}

	var res struct {
		TransactionID string `json:"transaction_id"`
		TradeState    string `json:"trade_state"`
		Amount        struct {
			Total    int64  `json:"total"`
			Currency string `json:"currency"`
		} `json:"amount"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return StatusResult{}, err
	}

	status := StatusPending
	if res.TradeState == "SUCCESS" {
		status = StatusSucceeded
	}

	return StatusResult{
		ProviderTxnID: res.TransactionID,
		Status:        status,
		AmountCents:   res.Amount.Total,
		Currency:      strings.ToUpper(res.Amount.Currency),
		PaidAt:        time.Now().UTC(),
	}, nil
}

// Refund initiates a WeChat Pay refund.
func (w *WeChatPay) Refund(ctx context.Context, params RefundParams) (RefundResult, error) {
	path := "/v3/refund/domestic/refunds"
	if params.AmountCents <= 0 {
		return RefundResult{}, errors.New("refund amount must be positive")
	}
	// WeChat requires amount.total alongside amount.refund. Falling back to
	// the refund amount would silently turn a partial refund into a full
	// refund, so the original total is mandatory.
	total := params.OriginalAmountCents
	if total <= 0 {
		return RefundResult{}, errors.New("original order amount required for wechat refund")
	}
	if params.AmountCents > total {
		return RefundResult{}, fmt.Errorf("refund amount %d exceeds original total %d", params.AmountCents, total)
	}
	reqBody := map[string]any{
		"out_trade_no":  params.OrderNo,
		"out_refund_no": params.RefundNo,
		"reason":        params.Reason,
		"amount": map[string]any{
			"refund":   params.AmountCents,
			"total":    total,
			"currency": strings.ToUpper(params.Currency),
		},
	}
	if params.ProviderTxnID != "" {
		reqBody["transaction_id"] = params.ProviderTxnID
	}

	rawReq, err := json.Marshal(reqBody)
	if err != nil {
		return RefundResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(w.Base, "/")+path, bytes.NewReader(rawReq))
	if err != nil {
		return RefundResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	auth, err := w.buildAuthHeader(http.MethodPost, path, rawReq)
	if err != nil {
		return RefundResult{}, err
	}
	req.Header.Set("Authorization", auth)

	resp, err := w.Client.Do(req)
	if err != nil {
		return RefundResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RefundResult{}, fmt.Errorf("wechat refund: status %d: %s", resp.StatusCode, briefBody(body))
	}

	var res struct {
		RefundID string `json:"refund_id"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return RefundResult{}, err
	}

	return RefundResult{
		ProviderRefundID: res.RefundID,
		Status:           strings.ToLower(res.Status),
	}, nil
}
