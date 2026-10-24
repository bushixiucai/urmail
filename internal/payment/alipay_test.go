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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAlipayProviderName(t *testing.T) {
	a := &Alipay{}
	if a.Name() != ProviderAlipay {
		t.Fatalf("expected name %s, got %s", ProviderAlipay, a.Name())
	}
}

func TestAlipayFullFlow(t *testing.T) {
	merchantKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate merchant key: %v", err)
	}
	alipayKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate alipay key: %v", err)
	}

	appID := "20210001"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("failed to parse form: %v", err)
		}

		// Verify merchant request signature using merchant public key
		formMap := make(map[string]string)
		for k, v := range r.Form {
			if len(v) > 0 {
				formMap[k] = v[0]
			}
		}
		sig := formMap["sign"]
		if sig == "" {
			t.Errorf("missing signature in request")
		}
		if err := VerifyRSASHA256(BuildSignString(formMap), sig, &merchantKey.PublicKey); err != nil {
			t.Errorf("merchant request signature verification failed: %v", err)
		}

		method := formMap["method"]
		bizContentStr := formMap["biz_content"]
		var biz map[string]any
		_ = json.Unmarshal([]byte(bizContentStr), &biz)

		outTradeNo, _ := biz["out_trade_no"].(string)

		switch method {
		case "alipay.trade.precreate":
			if outTradeNo == "ORD-ERROR-GATEWAY" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"alipay_trade_precreate_response": {
						"code": "40004",
						"msg": "Business Failed",
						"sub_code": "ACQ.PAYMENT_AUTH_CODE_INVALID",
						"sub_msg": "支付授权码无效"
					}
				}`))
				return
			}
			if outTradeNo == "ORD-HTTP-500" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("internal server error"))
				return
			}
			if outTradeNo == "ORD-EMPTY-QR" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"alipay_trade_precreate_response": {
						"code": "10000",
						"msg": "Success",
						"out_trade_no": "ORD-EMPTY-QR",
						"qr_code": ""
					}
				}`))
				return
			}

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"alipay_trade_precreate_response": {
					"code": "10000",
					"msg": "Success",
					"out_trade_no": "ORD-ALI-001",
					"qr_code": "https://qr.alipay.com/bax12345"
				}
			}`))

		case "alipay.trade.query":
			if outTradeNo == "ORD-QUERY-FAIL" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"alipay_trade_query_response": {
						"code": "40004",
						"msg": "Business Failed",
						"sub_code": "ACQ.TRADE_NOT_EXIST",
						"sub_msg": "交易不存在"
					}
				}`))
				return
			}
			if outTradeNo == "ORD-PENDING" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"alipay_trade_query_response": {
						"code": "10000",
						"msg": "Success",
						"out_trade_no": "ORD-PENDING",
						"trade_no": "202610232200140002",
						"trade_status": "WAIT_BUYER_PAY",
						"total_amount": "50.00",
						"seller_id": "2088101122136241"
					}
				}`))
				return
			}
			if outTradeNo == "ORD-CLOSED" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"alipay_trade_query_response": {
						"code": "10000",
						"msg": "Success",
						"out_trade_no": "ORD-CLOSED",
						"trade_no": "202610232200140003",
						"trade_status": "TRADE_CLOSED",
						"total_amount": "50.00",
						"seller_id": "2088101122136241"
					}
				}`))
				return
			}

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"alipay_trade_query_response": {
					"code": "10000",
					"msg": "Success",
					"out_trade_no": "ORD-ALI-001",
					"trade_no": "202610232200140001",
					"trade_status": "TRADE_SUCCESS",
					"total_amount": "60.00",
					"seller_id": "2088101122136241",
					"send_pay_date": "2026-10-23 14:30:00"
				}
			}`))

		case "alipay.trade.refund":
			if outTradeNo == "ORD-REFUND-FAIL" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"alipay_trade_refund_response": {
						"code": "40004",
						"msg": "Business Failed",
						"sub_code": "ACQ.TRADE_HAS_FINISHED",
						"sub_msg": "交易已结束"
					}
				}`))
				return
			}

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"alipay_trade_refund_response": {
					"code": "10000",
					"msg": "Success",
					"trade_no": "202610232200140001",
					"out_trade_no": "ORD-ALI-001",
					"out_request_no": "REF-ALI-001",
					"fund_change": "Y",
					"refund_fee": "60.00"
				}
			}`))

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"error_response": {
					"code": "40001",
					"msg": "Missing Method",
					"sub_code": "isv.missing-method",
					"sub_msg": "缺少方法名参数"
				}
			}`))
		}
	}))
	defer srv.Close()

	client := NewAlipay(appID, merchantKey, &alipayKey.PublicKey)
	client.SellerID = "2088101122136241"
	client.Base = srv.URL

	t.Run("CreateCheckout success", func(t *testing.T) {
		res, err := client.CreateCheckout(context.Background(), CheckoutParams{
			OrderID:     42,
			OrderNo:     "ORD-ALI-001",
			AmountCents: 6000,
			Currency:    "CNY",
			Description: "Pro Plan",
		})
		if err != nil {
			t.Fatalf("CreateCheckout failed: %v", err)
		}
		if res.QRCode != "https://qr.alipay.com/bax12345" {
			t.Errorf("unexpected qr_code: %s", res.QRCode)
		}
		if res.URL != "https://qr.alipay.com/bax12345" {
			t.Errorf("unexpected URL: %s", res.URL)
		}
		if res.SessionID != "ORD-ALI-001" {
			t.Errorf("unexpected sessionID: %s", res.SessionID)
		}
	})

	t.Run("CreateCheckout gateway error", func(t *testing.T) {
		_, err := client.CreateCheckout(context.Background(), CheckoutParams{
			OrderID:     42,
			OrderNo:     "ORD-ERROR-GATEWAY",
			Currency:    "CNY",
			AmountCents: 6000,
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "ACQ.PAYMENT_AUTH_CODE_INVALID") {
			t.Errorf("expected error containing sub_code, got: %v", err)
		}
	})

	t.Run("CreateCheckout HTTP 500", func(t *testing.T) {
		_, err := client.CreateCheckout(context.Background(), CheckoutParams{
			OrderNo:     "ORD-HTTP-500",
			Currency:    "CNY",
			AmountCents: 1000,
		})
		if err == nil {
			t.Fatal("expected error on http 500, got nil")
		}
	})

	t.Run("CreateCheckout empty qr_code", func(t *testing.T) {
		_, err := client.CreateCheckout(context.Background(), CheckoutParams{
			OrderNo:     "ORD-EMPTY-QR",
			Currency:    "CNY",
			AmountCents: 1000,
		})
		if err == nil {
			t.Fatal("expected error on empty qr_code, got nil")
		}
	})

	t.Run("CreatePagePayURL returns signed link", func(t *testing.T) {
		urlStr, err := client.CreatePagePayURL(CheckoutParams{
			OrderID:     42,
			OrderNo:     "ORD-ALI-PAGE",
			AmountCents: 6000,
			Currency:    "CNY",
			Description: "Pro Plan",
			SuccessURL:  "https://example.com/success",
		})
		if err != nil {
			t.Fatalf("CreatePagePayURL failed: %v", err)
		}
		parsed, err := url.Parse(urlStr)
		if err != nil {
			t.Fatalf("failed to parse url: %v", err)
		}
		q := parsed.Query()
		if q.Get("method") != "alipay.trade.page.pay" {
			t.Errorf("unexpected method: %s", q.Get("method"))
		}
		if q.Get("sign") == "" {
			t.Error("missing sign in generated URL")
		}
		if q.Get("return_url") != "https://example.com/success" {
			t.Errorf("unexpected return_url: %s", q.Get("return_url"))
		}
	})

	// Helper to sign webhook parameters with Alipay private key
	signWithAlipayKey := func(m map[string]string) string {
		content := BuildSignString(m)
		h := sha256.Sum256([]byte(content))
		sig, err := rsa.SignPKCS1v15(rand.Reader, alipayKey, crypto.SHA256, h[:])
		if err != nil {
			t.Fatalf("signWithAlipayKey failed: %v", err)
		}
		return base64.StdEncoding.EncodeToString(sig)
	}

	t.Run("ParseAndVerifyWebhook success TRADE_SUCCESS", func(t *testing.T) {
		params := map[string]string{
			"app_id":          appID,
			"seller_id":       "2088101122136241",
			"trade_no":        "202610232200140001",
			"out_trade_no":    "ORD-ALI-001",
			"trade_status":    "TRADE_SUCCESS",
			"total_amount":    "60.00",
			"notify_id":       "NOTIFY-20261023-01",
			"notify_type":     "trade_status_sync",
			"notify_time":     "2026-10-23 14:30:00",
			"gmt_payment":     "2026-10-23 14:30:00",
			"passback_params": "42",
		}
		params["sign"] = signWithAlipayKey(params)
		params["sign_type"] = "RSA2"

		vals := url.Values{}
		for k, v := range params {
			vals.Set(k, v)
		}
		body := []byte(vals.Encode())

		event, err := client.ParseAndVerifyWebhook(context.Background(), nil, body)
		if err != nil {
			t.Fatalf("ParseAndVerifyWebhook failed: %v", err)
		}

		if event.Provider != ProviderAlipay {
			t.Errorf("expected provider %s, got %s", ProviderAlipay, event.Provider)
		}
		if event.OrderID != 42 {
			t.Errorf("expected orderID 42, got %d", event.OrderID)
		}
		if event.OrderNo != "ORD-ALI-001" {
			t.Errorf("expected orderNo ORD-ALI-001, got %s", event.OrderNo)
		}
		if event.ProviderTxnID != "202610232200140001" {
			t.Errorf("expected txnID 202610232200140001, got %s", event.ProviderTxnID)
		}
		if event.AmountCents != 6000 {
			t.Errorf("expected amountCents 6000, got %d", event.AmountCents)
		}
		if event.Currency != "CNY" {
			t.Errorf("expected currency CNY, got %s", event.Currency)
		}
		if event.Status != StatusSucceeded {
			t.Errorf("expected status %s, got %s", StatusSucceeded, event.Status)
		}
		if event.PaidAt.IsZero() {
			t.Errorf("expected non-zero PaidAt")
		}
	})

	t.Run("ParseAndVerifyWebhook success TRADE_FINISHED", func(t *testing.T) {
		params := map[string]string{
			"app_id":          appID,
			"seller_id":       "2088101122136241",
			"trade_no":        "202610232200140005",
			"out_trade_no":    "ORD-ALI-005",
			"trade_status":    "TRADE_FINISHED",
			"total_amount":    "12.34",
			"passback_params": "50",
		}
		params["sign"] = signWithAlipayKey(params)
		params["sign_type"] = "RSA2"

		vals := url.Values{}
		for k, v := range params {
			vals.Set(k, v)
		}

		event, err := client.ParseAndVerifyWebhook(context.Background(), nil, []byte(vals.Encode()))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if event.Status != StatusSucceeded {
			t.Errorf("expected succeeded status, got %s", event.Status)
		}
		if event.AmountCents != 1234 {
			t.Errorf("expected 1234 cents, got %d", event.AmountCents)
		}
	})

	t.Run("ParseAndVerifyWebhook success with JSON payload", func(t *testing.T) {
		params := map[string]string{
			"app_id":          appID,
			"seller_id":       "2088101122136241",
			"trade_no":        "202610232200140009",
			"out_trade_no":    "ORD-ALI-009",
			"trade_status":    "TRADE_SUCCESS",
			"total_amount":    "99.00",
			"passback_params": "99",
		}
		params["sign"] = signWithAlipayKey(params)
		params["sign_type"] = "RSA2"

		jsonBytes, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}

		event, err := client.ParseAndVerifyWebhook(context.Background(), nil, jsonBytes)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if event.OrderNo != "ORD-ALI-009" || event.AmountCents != 9900 {
			t.Errorf("unexpected event: %+v", event)
		}
	})

	t.Run("ParseAndVerifyWebhook rejects uncompleted WAIT_BUYER_PAY", func(t *testing.T) {
		params := map[string]string{
			"app_id":       appID,
			"seller_id":    "2088101122136241",
			"trade_no":     "202610232200140002",
			"out_trade_no": "ORD-ALI-002",
			"trade_status": "WAIT_BUYER_PAY",
			"total_amount": "50.00",
		}
		params["sign"] = signWithAlipayKey(params)
		params["sign_type"] = "RSA2"

		vals := url.Values{}
		for k, v := range params {
			vals.Set(k, v)
		}

		_, err := client.ParseAndVerifyWebhook(context.Background(), nil, []byte(vals.Encode()))
		if !errors.Is(err, ErrPaymentNotPaid) {
			t.Fatalf("expected ErrPaymentNotPaid, got %v", err)
		}
	})

	t.Run("ParseAndVerifyWebhook rejects TRADE_CLOSED", func(t *testing.T) {
		params := map[string]string{
			"app_id":       appID,
			"seller_id":    "2088101122136241",
			"trade_no":     "202610232200140003",
			"out_trade_no": "ORD-ALI-003",
			"trade_status": "TRADE_CLOSED",
			"total_amount": "50.00",
		}
		params["sign"] = signWithAlipayKey(params)
		params["sign_type"] = "RSA2"

		vals := url.Values{}
		for k, v := range params {
			vals.Set(k, v)
		}

		_, err := client.ParseAndVerifyWebhook(context.Background(), nil, []byte(vals.Encode()))
		if !errors.Is(err, ErrPaymentNotPaid) {
			t.Fatalf("expected ErrPaymentNotPaid, got %v", err)
		}
	})

	t.Run("ParseAndVerifyWebhook rejects invalid signature", func(t *testing.T) {
		params := map[string]string{
			"app_id":       appID,
			"trade_no":     "202610232200140001",
			"out_trade_no": "ORD-ALI-001",
			"trade_status": "TRADE_SUCCESS",
			"total_amount": "60.00",
			"sign":         "d3Jvbmc=",
			"sign_type":    "RSA2",
		}
		vals := url.Values{}
		for k, v := range params {
			vals.Set(k, v)
		}

		_, err := client.ParseAndVerifyWebhook(context.Background(), nil, []byte(vals.Encode()))
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("expected ErrInvalidSignature, got %v", err)
		}
	})

	t.Run("ParseAndVerifyWebhook rejects missing signature", func(t *testing.T) {
		params := map[string]string{
			"app_id":       appID,
			"trade_status": "TRADE_SUCCESS",
			"total_amount": "60.00",
		}
		vals := url.Values{}
		for k, v := range params {
			vals.Set(k, v)
		}

		_, err := client.ParseAndVerifyWebhook(context.Background(), nil, []byte(vals.Encode()))
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("expected ErrInvalidSignature, got %v", err)
		}
	})

	t.Run("ParseAndVerifyWebhook rejects app_id mismatch", func(t *testing.T) {
		params := map[string]string{
			"app_id":       "different-app-id",
			"trade_no":     "202610232200140001",
			"out_trade_no": "ORD-ALI-001",
			"trade_status": "TRADE_SUCCESS",
			"total_amount": "60.00",
		}
		params["sign"] = signWithAlipayKey(params)
		params["sign_type"] = "RSA2"

		vals := url.Values{}
		for k, v := range params {
			vals.Set(k, v)
		}

		_, err := client.ParseAndVerifyWebhook(context.Background(), nil, []byte(vals.Encode()))
		if !errors.Is(err, ErrMerchantMismatch) {
			t.Fatalf("expected ErrMerchantMismatch, got %v", err)
		}
	})

	t.Run("QueryPayment success", func(t *testing.T) {
		status, err := client.QueryPayment(context.Background(), "ORD-ALI-001", "")
		if err != nil {
			t.Fatalf("QueryPayment failed: %v", err)
		}
		if status.Status != StatusSucceeded {
			t.Errorf("expected succeeded status, got %s", status.Status)
		}
		if status.AmountCents != 6000 {
			t.Errorf("expected amount 6000, got %d", status.AmountCents)
		}
		if status.ProviderTxnID != "202610232200140001" {
			t.Errorf("expected txnID 202610232200140001, got %s", status.ProviderTxnID)
		}
		if status.Currency != "CNY" {
			t.Errorf("expected currency CNY, got %s", status.Currency)
		}
	})

	t.Run("QueryPayment pending", func(t *testing.T) {
		status, err := client.QueryPayment(context.Background(), "ORD-PENDING", "")
		if err != nil {
			t.Fatalf("QueryPayment failed: %v", err)
		}
		if status.Status != StatusPending {
			t.Errorf("expected pending status, got %s", status.Status)
		}
		if status.AmountCents != 5000 {
			t.Errorf("expected amount 5000, got %d", status.AmountCents)
		}
	})

	t.Run("QueryPayment closed", func(t *testing.T) {
		status, err := client.QueryPayment(context.Background(), "ORD-CLOSED", "")
		if err != nil {
			t.Fatalf("QueryPayment failed: %v", err)
		}
		if status.Status != StatusFailed {
			t.Errorf("expected failed status for closed trade, got %s", status.Status)
		}
	})

	t.Run("QueryPayment failure", func(t *testing.T) {
		_, err := client.QueryPayment(context.Background(), "ORD-QUERY-FAIL", "")
		if err == nil {
			t.Fatal("expected query error, got nil")
		}
	})

	t.Run("QueryPayment empty params", func(t *testing.T) {
		_, err := client.QueryPayment(context.Background(), "", "")
		if err == nil {
			t.Fatal("expected error with empty orderNo and txnID, got nil")
		}
	})

	t.Run("Refund success", func(t *testing.T) {
		refund, err := client.Refund(context.Background(), RefundParams{
			OrderNo:     "ORD-ALI-001",
			RefundNo:    "REF-ALI-001",
			AmountCents: 6000,
			Currency:    "CNY",
			Reason:      "Customer request",
		})
		if err != nil {
			t.Fatalf("Refund failed: %v", err)
		}
		if refund.ProviderRefundID != "202610232200140001" {
			t.Errorf("unexpected refund ID: %s", refund.ProviderRefundID)
		}
		if refund.Status != StatusSucceeded {
			t.Errorf("unexpected refund status: %s", refund.Status)
		}
	})

	t.Run("Refund failure", func(t *testing.T) {
		_, err := client.Refund(context.Background(), RefundParams{
			OrderNo:     "ORD-REFUND-FAIL",
			AmountCents: 6000,
		})
		if err == nil {
			t.Fatal("expected refund error, got nil")
		}
	})

	t.Run("Refund empty params", func(t *testing.T) {
		_, err := client.Refund(context.Background(), RefundParams{})
		if err == nil {
			t.Fatal("expected error with empty orderNo and txnID, got nil")
		}
	})
}

func TestSignAndVerifyHelpers(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key failed: %v", err)
	}

	params := map[string]string{
		"app_id":    "20210001",
		"method":    "alipay.trade.query",
		"charset":   "utf-8",
		"sign":      "should_be_ignored",
		"sign_type": "RSA2",
		"empty_val": "",
	}

	content := BuildSignString(params)
	expected := "app_id=20210001&charset=utf-8&method=alipay.trade.query"
	if content != expected {
		t.Errorf("expected sign string %q, got %q", expected, content)
	}

	sig, err := SignRSASHA256(content, key)
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	if err := VerifyRSASHA256(content, sig, &key.PublicKey); err != nil {
		t.Fatalf("verify failed: %v", err)
	}

	// Tampered content
	if err := VerifyRSASHA256(content+"tampered", sig, &key.PublicKey); err == nil {
		t.Fatal("expected verification to fail for tampered content")
	}

	// Wrong key
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	if err := VerifyRSASHA256(content, sig, &otherKey.PublicKey); err == nil {
		t.Fatal("expected verification to fail with different public key")
	}
}

func TestPEMKeyParsing(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// PKCS1 Private Key
	pkcs1Der := x509.MarshalPKCS1PrivateKey(key)
	pkcs1PEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: pkcs1Der,
	})

	parsedPriv1, err := ParseRSAPrivateKeyFromPEM(pkcs1PEM)
	if err != nil {
		t.Fatalf("failed to parse PKCS1 private key: %v", err)
	}
	if parsedPriv1.N.Cmp(key.N) != 0 {
		t.Error("parsed PKCS1 key mismatch")
	}

	// PKCS8 Private Key
	pkcs8Der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal PKCS8: %v", err)
	}
	pkcs8PEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8Der,
	})

	parsedPriv8, err := ParseRSAPrivateKeyFromPEM(pkcs8PEM)
	if err != nil {
		t.Fatalf("failed to parse PKCS8 private key: %v", err)
	}
	if parsedPriv8.N.Cmp(key.N) != 0 {
		t.Error("parsed PKCS8 key mismatch")
	}

	// PKIX Public Key
	pkixDer, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal PKIX: %v", err)
	}
	pkixPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pkixDer,
	})

	parsedPub, err := ParseRSAPublicKeyFromPEM(pkixPEM)
	if err != nil {
		t.Fatalf("failed to parse PKIX public key: %v", err)
	}
	if parsedPub.N.Cmp(key.N) != 0 {
		t.Error("parsed public key mismatch")
	}
}

func TestAmountAndDateHelpers(t *testing.T) {
	tests := []struct {
		cents int64
		yuan  string
	}{
		{6000, "60.00"},
		{1, "0.01"},
		{990, "9.90"},
		{123456, "1234.56"},
		{0, "0.00"},
	}

	for _, tt := range tests {
		formatted := formatCentsToYuan(tt.cents)
		if formatted != tt.yuan {
			t.Errorf("formatCentsToYuan(%d) = %s, want %s", tt.cents, formatted, tt.yuan)
		}

		parsed, err := parseYuanToCents(tt.yuan)
		if err != nil {
			t.Errorf("parseYuanToCents(%s) error: %v", tt.yuan, err)
		}
		if parsed != tt.cents {
			t.Errorf("parseYuanToCents(%s) = %d, want %d", tt.yuan, parsed, tt.cents)
		}
	}

	// Test partial decimal cases
	parsed, err := parseYuanToCents("9.9")
	if err != nil || parsed != 990 {
		t.Errorf("parseYuanToCents(9.9) = %d, err = %v, want 990", parsed, err)
	}
	parsed, err = parseYuanToCents("100")
	if err != nil || parsed != 10000 {
		t.Errorf("parseYuanToCents(100) = %d, err = %v, want 10000", parsed, err)
	}

	// Time helper
	parsedTime := parseAlipayTime("2026-10-23 14:30:00")
	if parsedTime.Year() != 2026 || parsedTime.Month() != 10 || parsedTime.Day() != 23 {
		t.Errorf("unexpected parsed time: %v", parsedTime)
	}
}

func TestAlipayWebhookSellerIDCheck(t *testing.T) {
	merchantKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate merchant key: %v", err)
	}
	alipayKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate alipay key: %v", err)
	}

	buildBody := func(t *testing.T, params map[string]string) []byte {
		t.Helper()
		content := BuildSignString(params)
		h := sha256.Sum256([]byte(content))
		sig, err := rsa.SignPKCS1v15(rand.Reader, alipayKey, crypto.SHA256, h[:])
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		params["sign"] = base64.StdEncoding.EncodeToString(sig)
		params["sign_type"] = "RSA2"
		vals := url.Values{}
		for k, v := range params {
			vals.Set(k, v)
		}
		return []byte(vals.Encode())
	}

	base := map[string]string{
		"app_id":       "2021000000000001",
		"trade_no":     "202610232200149999",
		"out_trade_no": "ORD-ALI-SELLER",
		"trade_status": "TRADE_SUCCESS",
		"total_amount": "1.00",
	}

	t.Run("missing seller_id rejected", func(t *testing.T) {
		client := NewAlipay("2021000000000001", merchantKey, &alipayKey.PublicKey)
		client.SellerID = "2088000000000001"
		params := make(map[string]string, len(base))
		for k, v := range base {
			params[k] = v
		}
		_, err := client.ParseAndVerifyWebhook(context.Background(), nil, buildBody(t, params))
		if !errors.Is(err, ErrMerchantMismatch) {
			t.Fatalf("expected ErrMerchantMismatch for missing seller_id, got %v", err)
		}
	})

	t.Run("wrong seller_id rejected", func(t *testing.T) {
		client := NewAlipay("2021000000000001", merchantKey, &alipayKey.PublicKey)
		client.SellerID = "2088000000000001"
		params := map[string]string{"seller_id": "2088999999999999"}
		for k, v := range base {
			params[k] = v
		}
		_, err := client.ParseAndVerifyWebhook(context.Background(), nil, buildBody(t, params))
		if !errors.Is(err, ErrMerchantMismatch) {
			t.Fatalf("expected ErrMerchantMismatch for wrong seller_id, got %v", err)
		}
	})

	t.Run("matching seller_id accepted", func(t *testing.T) {
		client := NewAlipay("2021000000000001", merchantKey, &alipayKey.PublicKey)
		client.SellerID = "2088000000000001"
		params := map[string]string{"seller_id": "2088000000000001"}
		for k, v := range base {
			params[k] = v
		}
		event, err := client.ParseAndVerifyWebhook(context.Background(), nil, buildBody(t, params))
		if err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		if event.Status != StatusSucceeded {
			t.Errorf("unexpected status: %s", event.Status)
		}
	})

	t.Run("no configured seller_id rejected", func(t *testing.T) {
		client := NewAlipay("2021000000000001", merchantKey, &alipayKey.PublicKey)
		params := map[string]string{"seller_id": "2088999999999999"}
		for k, v := range base {
			params[k] = v
		}
		if _, err := client.ParseAndVerifyWebhook(context.Background(), nil, buildBody(t, params)); !errors.Is(err, ErrMerchantMismatch) {
			t.Fatalf("expected fail-closed ErrMerchantMismatch, got %v", err)
		}
	})
}

func TestAlipayQuerySellerIDCheck(t *testing.T) {
	merchantKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate merchant key: %v", err)
	}
	alipayKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate alipay key: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"alipay_trade_query_response":{"code":"10000","msg":"Success","trade_no":"202610232200148888","out_trade_no":"ORD-Q","trade_status":"TRADE_SUCCESS","total_amount":"1.00","seller_id":"2088999999999999"}}`))
	}))
	defer srv.Close()

	client := NewAlipay("2021000000000001", merchantKey, &alipayKey.PublicKey)
	client.Base = srv.URL
	client.SellerID = "2088000000000001"

	if _, err := client.QueryPayment(context.Background(), "ORD-Q", ""); !errors.Is(err, ErrMerchantMismatch) {
		t.Fatalf("expected ErrMerchantMismatch for seller_id mismatch on query, got %v", err)
	}
}

func TestAlipayRefundFundChange(t *testing.T) {
	merchantKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate merchant key: %v", err)
	}
	alipayKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate alipay key: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"alipay_trade_refund_response":{"code":"10000","msg":"Success","trade_no":"202610232200147777","out_request_no":"REF-FC","fund_change":"N","refund_fee":"1.00"}}`))
	}))
	defer srv.Close()

	client := NewAlipay("2021000000000001", merchantKey, &alipayKey.PublicKey)
	client.Base = srv.URL

	res, err := client.Refund(context.Background(), RefundParams{OrderNo: "ORD-FC", RefundNo: "REF-FC", AmountCents: 100})
	if err != nil {
		t.Fatalf("Refund failed: %v", err)
	}
	if res.Status != "processing" {
		t.Fatalf("expected pending status when fund_change != Y, got %q", res.Status)
	}
}
