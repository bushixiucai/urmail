package payment

import (
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
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helper to encrypt with AEAD_AES_256_GCM
func encryptGCM(key []byte, nonce []byte, plaintext []byte, associatedData []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ciphertext := aesGCM.Seal(nil, nonce, plaintext, associatedData)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func TestWeChatPayFullFlow(t *testing.T) {
	merchantKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate merchant key: %v", err)
	}
	platformKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate platform key: %v", err)
	}

	apiv3Key := "12345678901234567890123456789012" // 32 bytes

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "WECHATPAY2-SHA256-RSA2048 ") {
			t.Errorf("missing or invalid wechat auth header: %s", auth)
		}

		switch {
		case r.URL.Path == "/v3/pay/transactions/native":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"code_url":"weixin://wxpay/bizpayurl?pr=xyz123"}`))
		case strings.HasPrefix(r.URL.Path, "/v3/pay/transactions/out-trade-no/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"transaction_id": "4200001234",
				"trade_state": "SUCCESS",
				"amount": {"total": 6000, "currency": "CNY"}
			}`))
		case r.URL.Path == "/v3/refund/domestic/refunds":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"refund_id": "5000001234",
				"status": "SUCCESS"
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	wc := NewWeChatPay("1900000109", "wx1234567890", apiv3Key, "CERT123456", merchantKey, &platformKey.PublicKey)
	wc.Base = srv.URL

	t.Run("CreateCheckout returns QR code", func(t *testing.T) {
		res, err := wc.CreateCheckout(context.Background(), CheckoutParams{
			OrderID:     42,
			OrderNo:     "ORD-WX-001",
			AmountCents: 6000,
			Currency:    "CNY",
			Description: "Pro Plan",
		})
		if err != nil {
			t.Fatalf("CreateCheckout failed: %v", err)
		}
		if res.QRCode != "weixin://wxpay/bizpayurl?pr=xyz123" {
			t.Errorf("unexpected code url: %s", res.QRCode)
		}
		if res.SessionID != "ORD-WX-001" {
			t.Errorf("unexpected session ID: %s", res.SessionID)
		}
	})

	t.Run("ParseAndVerifyWebhook success", func(t *testing.T) {
		txnPlain := []byte(`{
			"mchid": "1900000109",
			"appid": "wx1234567890",
			"out_trade_no": "ORD-WX-001",
			"transaction_id": "4200001234",
			"trade_state": "SUCCESS",
			"success_time": "2026-10-23T14:30:00+08:00",
			"attach": "42",
			"amount": {"total": 6000, "currency": "CNY"}
		}`)

		nonce := "123456789012"
		associatedData := "transaction"
		cipherB64, err := encryptGCM([]byte(apiv3Key), []byte(nonce), txnPlain, []byte(associatedData))
		if err != nil {
			t.Fatalf("failed to encrypt GCM: %v", err)
		}

		notificationBody, _ := json.Marshal(map[string]any{
			"id":            "EVT-001",
			"event_type":    "TRANSACTION.SUCCESS",
			"resource_type": "encrypt-resource",
			"resource": map[string]any{
				"algorithm":       "AEAD_AES_256_GCM",
				"ciphertext":      cipherB64,
				"nonce":           nonce,
				"associated_data": associatedData,
			},
		})

		ts := strconv.FormatInt(time.Now().Unix(), 10)
		message := ts + "\n" + nonce + "\n" + string(notificationBody) + "\n"
		hashed := sha256.Sum256([]byte(message))
		sigBytes, err := rsa.SignPKCS1v15(rand.Reader, platformKey, crypto.SHA256, hashed[:])
		if err != nil {
			t.Fatalf("failed to sign message: %v", err)
		}
		sig := base64.StdEncoding.EncodeToString(sigBytes)

		header := http.Header{}
		header.Set("Wechatpay-Timestamp", ts)
		header.Set("Wechatpay-Nonce", nonce)
		header.Set("Wechatpay-Signature", sig)

		event, err := wc.ParseAndVerifyWebhook(context.Background(), header, notificationBody)
		if err != nil {
			t.Fatalf("ParseAndVerifyWebhook failed: %v", err)
		}

		if event.Provider != ProviderWeChat {
			t.Errorf("expected provider wechat, got %s", event.Provider)
		}
		if event.OrderID != 42 {
			t.Errorf("expected orderID 42, got %d", event.OrderID)
		}
		if event.OrderNo != "ORD-WX-001" {
			t.Errorf("expected orderNo ORD-WX-001, got %s", event.OrderNo)
		}
		if event.ProviderTxnID != "4200001234" {
			t.Errorf("expected txnID 4200001234, got %s", event.ProviderTxnID)
		}
		if event.AmountCents != 6000 {
			t.Errorf("expected amount 6000, got %d", event.AmountCents)
		}
		if event.Status != StatusSucceeded {
			t.Errorf("expected status succeeded, got %s", event.Status)
		}
	})

	t.Run("ParseAndVerifyWebhook rejects unpaid", func(t *testing.T) {
		txnPlain := []byte(`{
			"mchid": "1900000109",
			"appid": "wx1234567890",
			"out_trade_no": "ORD-WX-002",
			"transaction_id": "4200001235",
			"trade_state": "NOTPAY",
			"amount": {"total": 6000, "currency": "CNY"}
		}`)

		nonce := "123456789012"
		associatedData := "transaction"
		cipherB64, err := encryptGCM([]byte(apiv3Key), []byte(nonce), txnPlain, []byte(associatedData))
		if err != nil {
			t.Fatalf("failed to encrypt GCM: %v", err)
		}

		notificationBody, _ := json.Marshal(map[string]any{
			"id":            "EVT-002",
			"event_type":    "TRANSACTION.SUCCESS",
			"resource_type": "encrypt-resource",
			"resource": map[string]any{
				"algorithm":       "AEAD_AES_256_GCM",
				"ciphertext":      cipherB64,
				"nonce":           nonce,
				"associated_data": associatedData,
			},
		})

		ts := strconv.FormatInt(time.Now().Unix(), 10)
		header := http.Header{}
		header.Set("Wechatpay-Timestamp", ts)
		header.Set("Wechatpay-Nonce", nonce)

		message := ts + "\n" + nonce + "\n" + string(notificationBody) + "\n"
		hashed := sha256.Sum256([]byte(message))
		sigBytes, _ := rsa.SignPKCS1v15(rand.Reader, platformKey, crypto.SHA256, hashed[:])
		header.Set("Wechatpay-Signature", base64.StdEncoding.EncodeToString(sigBytes))

		_, err = wc.ParseAndVerifyWebhook(context.Background(), header, notificationBody)
		if !errors.Is(err, ErrPaymentNotPaid) {
			t.Fatalf("expected ErrPaymentNotPaid, got %v", err)
		}
	})

	t.Run("QueryPayment returns status result", func(t *testing.T) {
		status, err := wc.QueryPayment(context.Background(), "ORD-WX-001", "")
		if err != nil {
			t.Fatalf("QueryPayment failed: %v", err)
		}
		if status.Status != StatusSucceeded || status.AmountCents != 6000 || status.ProviderTxnID != "4200001234" {
			t.Errorf("unexpected query result: %+v", status)
		}
	})

	t.Run("Refund returns refund result", func(t *testing.T) {
		refund, err := wc.Refund(context.Background(), RefundParams{
			OrderNo:             "ORD-WX-001",
			RefundNo:            "REF-WX-001",
			AmountCents:         6000,
			OriginalAmountCents: 6000,
			Currency:            "CNY",
			Reason:              "Customer request",
		})
		if err != nil {
			t.Fatalf("Refund failed: %v", err)
		}
		if refund.ProviderRefundID != "5000001234" || refund.Status != StatusSucceeded {
			t.Errorf("unexpected refund result: %+v", refund)
		}
	})
}

func TestWeChatPayWebhookRequiresPlatformKey(t *testing.T) {
	wc := NewWeChatPay("1900000109", "wx1234567890", "12345678901234567890123456789012", "CERT123456", nil, nil)
	header := http.Header{}
	header.Set("Wechatpay-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	header.Set("Wechatpay-Nonce", "123456789012")
	header.Set("Wechatpay-Signature", "bm90LXJlYWw=")
	_, err := wc.ParseAndVerifyWebhook(context.Background(), header, []byte("{}"))
	if err == nil {
		t.Fatal("expected error when PlatformPubKey is nil, got nil")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

func TestWeChatPayWebhookMchidStrongCheck(t *testing.T) {
	merchantKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate merchant key: %v", err)
	}
	platformKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate platform key: %v", err)
	}
	apiv3Key := "12345678901234567890123456789012"

	build := func(wc *WeChatPay, mchid string) error {
		txnPlain, _ := json.Marshal(map[string]any{
			"mchid":        mchid,
			"appid":        "wx1234567890",
			"out_trade_no": "ORD-WX-MCH",
			"trade_state":  "SUCCESS",
			"amount":       map[string]any{"total": 100, "currency": "CNY"},
		})
		nonce := "123456789012"
		cipherB64, err := encryptGCM([]byte(apiv3Key), []byte(nonce), txnPlain, []byte("transaction"))
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		body, _ := json.Marshal(map[string]any{
			"id":         "EVT-MCH",
			"event_type": "TRANSACTION.SUCCESS",
			"resource": map[string]any{
				"algorithm":       "AEAD_AES_256_GCM",
				"ciphertext":      cipherB64,
				"nonce":           nonce,
				"associated_data": "transaction",
			},
		})
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		message := ts + "\n" + nonce + "\n" + string(body) + "\n"
		hashed := sha256.Sum256([]byte(message))
		sigBytes, _ := rsa.SignPKCS1v15(rand.Reader, platformKey, crypto.SHA256, hashed[:])
		header := http.Header{}
		header.Set("Wechatpay-Timestamp", ts)
		header.Set("Wechatpay-Nonce", nonce)
		header.Set("Wechatpay-Signature", base64.StdEncoding.EncodeToString(sigBytes))
		_, err = wc.ParseAndVerifyWebhook(context.Background(), header, body)
		return err
	}

	// mismatched mchid
	wc := NewWeChatPay("1900000109", "wx1234567890", apiv3Key, "CERT", merchantKey, &platformKey.PublicKey)
	if err := build(wc, "9999999999"); !errors.Is(err, ErrMerchantMismatch) {
		t.Fatalf("expected ErrMerchantMismatch for wrong mchid, got %v", err)
	}
	// missing callback mchid
	if err := build(wc, ""); !errors.Is(err, ErrMerchantMismatch) {
		t.Fatalf("expected ErrMerchantMismatch for empty callback mchid, got %v", err)
	}
	// missing configured mchid
	wcNoMch := NewWeChatPay("", "wx1234567890", apiv3Key, "CERT", merchantKey, &platformKey.PublicKey)
	if err := build(wcNoMch, "1900000109"); !errors.Is(err, ErrMerchantMismatch) {
		t.Fatalf("expected ErrMerchantMismatch for empty configured mchid, got %v", err)
	}
	// matching mchid passes signature/mchid stage
	if err := build(wc, "1900000109"); errors.Is(err, ErrMerchantMismatch) {
		t.Fatalf("did not expect ErrMerchantMismatch for matching mchid, got %v", err)
	}
}

func TestWeChatPayRefundAmountsAndOriginalTotal(t *testing.T) {
	merchantKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate merchant key: %v", err)
	}
	var gotAmount map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		gotAmount, _ = req["amount"].(map[string]any)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"refund_id":"5000009999","status":"PROCESSING"}`))
	}))
	defer srv.Close()

	wc := NewWeChatPay("1900000109", "wx1234567890", "12345678901234567890123456789012", "CERT", merchantKey, nil)
	wc.Base = srv.URL

	res, err := wc.Refund(context.Background(), RefundParams{
		OrderNo:             "ORD-WX-R1",
		RefundNo:            "REF-WX-R1",
		AmountCents:         2000,
		OriginalAmountCents: 6000,
		Currency:            "CNY",
	})
	if err != nil {
		t.Fatalf("Refund failed: %v", err)
	}
	if gotAmount["total"].(float64) != 6000 || gotAmount["refund"].(float64) != 2000 {
		t.Fatalf("expected total=6000 refund=2000, got %v", gotAmount)
	}
	if res.Status != "processing" {
		t.Errorf("unexpected status: %s", res.Status)
	}

	// zero/negative refund amount is rejected client-side
	if _, err := wc.Refund(context.Background(), RefundParams{OrderNo: "O", AmountCents: 0}); err == nil {
		t.Fatal("expected error for zero refund amount")
	}
}

func TestWeChatPayErrorTruncation(t *testing.T) {
	merchantKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate merchant key: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(strings.Repeat("x", 10000)))
	}))
	defer srv.Close()

	wc := NewWeChatPay("1900000109", "wx1234567890", "12345678901234567890123456789012", "CERT", merchantKey, nil)
	wc.Base = srv.URL
	_, err = wc.QueryPayment(context.Background(), "ORD-X", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if len(err.Error()) > 600 {
		t.Fatalf("error message too long (%d bytes), body not truncated: %v", len(err.Error()), err)
	}
}
