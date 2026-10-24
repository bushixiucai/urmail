package payment

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (w *WeChatPay) verifyResponse(header http.Header, body []byte) error {
	if w.PlatformPubKey == nil || w.PlatformKeyID == "" || header.Get("Wechatpay-Serial") != w.PlatformKeyID {
		return ErrInvalidSignature
	}
	timestamp := header.Get("Wechatpay-Timestamp")
	n, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || time.Since(time.Unix(n, 0)).Abs() > 5*time.Minute {
		return ErrSignatureExpired
	}
	nonce := header.Get("Wechatpay-Nonce")
	if nonce == "" {
		return ErrInvalidSignature
	}
	signature, err := base64.StdEncoding.DecodeString(header.Get("Wechatpay-Signature"))
	if err != nil {
		return ErrInvalidSignature
	}
	hash := sha256.Sum256([]byte(timestamp + "\n" + nonce + "\n" + string(body) + "\n"))
	if rsa.VerifyPKCS1v15(w.PlatformPubKey, crypto.SHA256, hash[:], signature) != nil {
		return ErrInvalidSignature
	}
	return nil
}
func (w *WeChatPay) requestLifecycle(ctx context.Context, method, path string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(w.Base, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	auth, err := w.buildAuthHeader(method, path, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")
	res, err := w.Client.Do(req)
	if err != nil {
		return nil, errors.New("wechat lifecycle request failed")
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, ErrInvalidPayload
	}
	if w.RequireResponseSignature {
		if err = w.verifyResponse(res.Header, raw); err != nil {
			return nil, err
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.New("wechat lifecycle request refused")
	}
	return raw, nil
}

// CloseCheckout uses WeChat's signed APIv3 close endpoint, without assuming timeout means closed.
func (w *WeChatPay) CloseCheckout(ctx context.Context, orderNo, _ string) error {
	if orderNo == "" {
		return ErrInvalidPayload
	}
	body, _ := json.Marshal(map[string]string{"mchid": w.MchID})
	_, err := w.requestLifecycle(ctx, http.MethodPost, "/v3/pay/transactions/out-trade-no/"+url.PathEscape(orderNo)+"/close", body)
	return err
}

// QueryRefund retrieves the existing merchant refund reference.
func (w *WeChatPay) QueryRefund(ctx context.Context, params RefundQueryParams) (RefundResult, error) {
	raw, err := w.requestLifecycle(ctx, http.MethodGet, "/v3/refund/domestic/refunds/"+url.PathEscape(params.RefundNo), nil)
	if err != nil {
		return RefundResult{}, err
	}
	var in struct {
		RefundID      string `json:"refund_id"`
		Status        string `json:"status"`
		TransactionID string `json:"transaction_id"`
		Amount        struct {
			Refund   int64  `json:"refund"`
			Currency string `json:"currency"`
		} `json:"amount"`
	}
	if json.Unmarshal(raw, &in) != nil || in.RefundID == "" {
		return RefundResult{}, ErrInvalidPayload
	}
	status := "processing"
	switch in.Status {
	case "SUCCESS":
		status = StatusSucceeded
	case "CLOSED":
		status = StatusFailed
	case "ABNORMAL":
		status = "uncertain"
	}
	return checkRefundQueryResult(RefundResult{ProviderRefundID: in.RefundID, Status: status, AmountCents: in.Amount.Refund, Currency: strings.ToUpper(in.Amount.Currency), ProviderTxnID: in.TransactionID}, params)
}

// CloseCheckout closes an unpaid Alipay trade through the RSA2 response-verifying executor.
func (a *Alipay) CloseCheckout(ctx context.Context, orderNo, _ string) error {
	if orderNo == "" {
		return ErrInvalidPayload
	}
	_, err := a.execute(ctx, "alipay.trade.close", map[string]string{"out_trade_no": orderNo})
	return err
}

// QueryRefund uses the stable out_request_no rather than creating a new refund.
func (a *Alipay) QueryRefund(ctx context.Context, params RefundQueryParams) (RefundResult, error) {
	raw, err := a.execute(ctx, "alipay.trade.fastpay.refund.query", map[string]string{"out_trade_no": params.OrderNo, "out_request_no": params.RefundNo})
	if err != nil {
		return RefundResult{}, err
	}
	var in struct {
		TradeNo      string `json:"trade_no"`
		RefundStatus string `json:"refund_status"`
		RefundAmount string `json:"refund_amount"`
	}
	if json.Unmarshal(raw, &in) != nil || in.TradeNo == "" {
		return RefundResult{}, ErrInvalidPayload
	}
	status := "processing"
	if in.RefundStatus == "REFUND_SUCCESS" {
		status = StatusSucceeded
	}
	amount := int64(0)
	if in.RefundAmount != "" {
		amount, err = parseYuanToCents(in.RefundAmount)
		if err != nil {
			return RefundResult{}, ErrInvalidPayload
		}
	}
	return checkRefundQueryResult(RefundResult{ProviderRefundID: params.RefundNo, Status: status, AmountCents: amount, Currency: "CNY", ProviderTxnID: in.TradeNo}, params)
}

// QueryRefund reads a Stripe refund or recovers its ID from the original intent and refund metadata.
func (s *Stripe) QueryRefund(ctx context.Context, params RefundQueryParams) (RefundResult, error) {
	refundID := params.ProviderRefundID
	if refundID == "" {
		if params.RefundNo == "" || !strings.HasPrefix(params.ProviderTxnID, "pi_") {
			return RefundResult{}, ErrUnsupportedOperation
		}
		after := ""
		for page := 0; page < 10; page++ {
			values := url.Values{"payment_intent": {params.ProviderTxnID}, "limit": {"100"}}
			if after != "" {
				values.Set("starting_after", after)
			}
			raw, err := s.refundQueryGET(ctx, "/v1/refunds?"+values.Encode())
			if err != nil {
				return RefundResult{}, err
			}
			var list struct {
				Data []struct {
					ID       string            `json:"id"`
					Metadata map[string]string `json:"metadata"`
				} `json:"data"`
				HasMore bool `json:"has_more"`
			}
			if json.Unmarshal(raw, &list) != nil {
				return RefundResult{}, ErrInvalidPayload
			}
			for _, candidate := range list.Data {
				if candidate.Metadata["refund_no"] == params.RefundNo {
					refundID = candidate.ID
					break
				}
			}
			if refundID != "" {
				break
			}
			if !list.HasMore || len(list.Data) == 0 {
				return RefundResult{}, ErrPaymentNotPaid
			}
			after = list.Data[len(list.Data)-1].ID
		}
	}
	if !strings.HasPrefix(refundID, "re_") {
		return RefundResult{}, ErrUnsupportedOperation
	}
	raw, err := s.refundQueryGET(ctx, "/v1/refunds/"+url.PathEscape(refundID))
	if err != nil {
		return RefundResult{}, err
	}
	var in struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		Amount        int64  `json:"amount"`
		Currency      string `json:"currency"`
		PaymentIntent string `json:"payment_intent"`
	}
	if json.Unmarshal(raw, &in) != nil || in.ID != refundID {
		return RefundResult{}, ErrInvalidPayload
	}
	return checkRefundQueryResult(RefundResult{ProviderRefundID: in.ID, Status: NormalizeRefundStatus(in.Status), AmountCents: in.Amount, Currency: strings.ToUpper(in.Currency), ProviderTxnID: in.PaymentIntent}, params)
}
func (s *Stripe) refundQueryGET(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.Base, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.SecretKey)
	response, err := s.Client.Do(req)
	if err != nil {
		return nil, errors.New("stripe refund query failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("stripe refund query refused")
	}
	return io.ReadAll(io.LimitReader(response.Body, 1<<20))
}

// QueryRefund reads a PayPal refund, or locates its invoice reference in the original order.
func (p *PayPal) QueryRefund(ctx context.Context, params RefundQueryParams) (RefundResult, error) {
	refundID := params.ProviderRefundID
	if refundID == "" {
		if params.SessionID == "" || params.RefundNo == "" {
			return RefundResult{}, ErrUnsupportedOperation
		}
		raw, err := p.refundQueryGET(ctx, "/v2/checkout/orders/"+url.PathEscape(params.SessionID))
		if err != nil {
			return RefundResult{}, err
		}
		var order struct {
			PurchaseUnits []struct {
				Payments struct {
					Refunds []struct {
						ID        string `json:"id"`
						InvoiceID string `json:"invoice_id"`
					} `json:"refunds"`
				} `json:"payments"`
			} `json:"purchase_units"`
		}
		if json.Unmarshal(raw, &order) != nil {
			return RefundResult{}, ErrInvalidPayload
		}
		candidates := 0
		for _, unit := range order.PurchaseUnits {
			for _, candidate := range unit.Payments.Refunds {
				candidates++
				if candidates > 100 {
					return RefundResult{}, ErrUnsupportedOperation
				}
				if candidate.InvoiceID == params.RefundNo {
					refundID = candidate.ID
					break
				}
				if candidate.InvoiceID == "" {
					body, e := p.refundQueryGET(ctx, "/v2/payments/refunds/"+url.PathEscape(candidate.ID))
					if e != nil {
						return RefundResult{}, e
					}
					var ref struct {
						InvoiceID string `json:"invoice_id"`
					}
					if json.Unmarshal(body, &ref) != nil {
						return RefundResult{}, ErrInvalidPayload
					}
					if ref.InvoiceID == params.RefundNo {
						refundID = candidate.ID
						break
					}
				}
			}
			if refundID != "" {
				break
			}
		}
		if refundID == "" {
			return RefundResult{}, ErrUnsupportedOperation
		}
	}
	raw, err := p.refundQueryGET(ctx, "/v2/payments/refunds/"+url.PathEscape(refundID))
	if err != nil {
		return RefundResult{}, err
	}
	var in struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Amount struct {
			Value    string `json:"value"`
			Currency string `json:"currency_code"`
		} `json:"amount"`
	}
	if json.Unmarshal(raw, &in) != nil || in.ID != refundID {
		return RefundResult{}, ErrInvalidPayload
	}
	amount := int64(0)
	if in.Amount.Value != "" {
		amount, err = parsePayPalAmount(in.Amount.Value, in.Amount.Currency)
		if err != nil {
			return RefundResult{}, ErrInvalidPayload
		}
	}
	return checkRefundQueryResult(RefundResult{ProviderRefundID: in.ID, Status: NormalizeRefundStatus(in.Status), AmountCents: amount, Currency: strings.ToUpper(in.Amount.Currency)}, params)
}
func (p *PayPal) refundQueryGET(ctx context.Context, path string) ([]byte, error) {
	token, err := p.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.Base, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := p.httpClient().Do(req)
	if err != nil {
		return nil, errors.New("paypal refund query failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("paypal refund query refused")
	}
	return io.ReadAll(io.LimitReader(response.Body, 1<<20))
}
func checkRefundQueryResult(result RefundResult, params RefundQueryParams) (RefundResult, error) {
	if result.Status != StatusSucceeded {
		return result, nil
	}
	if params.ExpectedAmountCents > 0 && (result.AmountCents != params.ExpectedAmountCents || result.AmountCents <= 0) {
		return result, ErrAmountMismatch
	}
	if params.ExpectedCurrency != "" && strings.ToUpper(params.ExpectedCurrency) != result.Currency {
		return result, ErrCurrencyMismatch
	}
	if params.ProviderTxnID != "" && result.ProviderTxnID != "" && params.ProviderTxnID != result.ProviderTxnID {
		return result, ErrMerchantMismatch
	}
	return result, nil
}
