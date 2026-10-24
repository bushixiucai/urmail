package payment

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type registryTransport struct {
	target *url.URL
	next   http.RoundTripper
}

func (r registryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u := *clone.URL
	u.Host = r.target.Host
	u.Scheme = r.target.Scheme
	clone.URL = &u
	return r.next.RoundTrip(clone)
}
func contractKeys(t *testing.T) (*rsa.PrivateKey, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
}
func TestRegistryCapabilitiesAndCredentialRotation(t *testing.T) {
	_, private, public := contractKeys(t)
	secrets := MerchantSecrets{AppID: "app", PrivateKeyPEM: private, ProviderPublicKeyPEM: public, APIv3Key: strings.Repeat("v", 32), CertificateSerial: "merchant-serial", ProviderKeyID: "platform-key"}
	m := Merchant{ID: 4, Provider: ProviderWeChat, Environment: "live", AccountID: "mch123", Name: "WeChat", Enabled: true, Currencies: []string{"CNY"}, Entrypoint: "native", Secrets: secrets}
	r := &Registry{BaseURL: "https://panel.example.test"}
	first, err := r.Build(m)
	if err != nil {
		t.Fatal(err)
	}
	wechat := first.(*WeChatPay)
	if wechat.NotifyURL != "https://panel.example.test/api/v1/payments/4/webhook" || !wechat.RequireResponseSignature {
		t.Fatal("registry did not wire callback or response verification")
	}
	scope := m.Scope()
	m.Secrets.CertificateSerial = "rotated"
	if m.Scope() != scope {
		t.Fatal("key rotation changed merchant scope")
	}
	m.Currencies = []string{"USD"}
	if m.Validate() == nil {
		t.Fatal("unsupported currency accepted")
	}
	for _, provider := range []string{ProviderAggregated, "tax_invoice"} {
		m.Provider = provider
		m.Enabled = true
		if m.Validate() == nil {
			t.Fatal("missing optional adapter enabled")
		}
	}
}
func TestAlipayRegistrySignedLifecycleContract(t *testing.T) {
	key, private, public := contractKeys(t)
	calls := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Error("expected gateway POST")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		params := map[string]string{}
		for k := range r.Form {
			params[k] = r.Form.Get(k)
		}
		if VerifyRSASHA256(BuildSignString(params), params["sign"], &key.PublicKey) != nil {
			t.Error("merchant RSA2 request signature invalid")
		}
		method := r.Form.Get("method")
		body := `{"code":"10000","out_trade_no":"ORD1","qr_code":"https://qr.alipay.com/test"}`
		switch method {
		case "alipay.trade.query":
			body = `{"code":"10000","trade_no":"TX1","out_trade_no":"ORD1","seller_id":"seller1","trade_status":"TRADE_SUCCESS","total_amount":"1.00"}`
		case "alipay.trade.refund":
			body = `{"code":"10000","trade_no":"TX1","out_trade_no":"ORD1","fund_change":"Y","refund_fee":"0.50"}`
		case "alipay.trade.fastpay.refund.query":
			body = `{"code":"10000","trade_no":"TX1","out_request_no":"REF1","refund_status":"REFUND_SUCCESS","refund_amount":"0.50"}`
		case "alipay.trade.close":
			body = `{"code":"10000","trade_no":"TX1","out_trade_no":"ORD1"}`
		}
		signature, err := SignRSASHA256(body, key)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(w, `{"%s":%s,"sign":%q}`, strings.ReplaceAll(method, ".", "_")+"_response", body, signature)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	reg := &Registry{BaseURL: "https://panel.example.test", Client: &http.Client{Transport: registryTransport{target: target, next: http.DefaultTransport}, Timeout: time.Second}}
	merchant := Merchant{ID: 1, Provider: ProviderAlipay, Environment: "sandbox", AccountID: "seller1", Name: "Alipay", Currencies: []string{"CNY"}, Entrypoint: "native", Secrets: MerchantSecrets{AppID: "app1", PrivateKeyPEM: private, ProviderPublicKeyPEM: public}}
	p, err := reg.Build(merchant)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	result, err := p.CreateCheckout(ctx, CheckoutParams{OrderID: 7, OrderNo: "ORD1", AmountCents: 100, Currency: "CNY"})
	if err != nil || ValidateCheckoutDestination(ProviderAlipay, result) != nil {
		t.Fatalf("checkout %+v %v", result, err)
	}
	status, err := p.QueryPayment(ctx, "ORD1", "")
	if err != nil || status.Status != StatusSucceeded || status.AmountCents != 100 {
		t.Fatalf("query %+v %v", status, err)
	}
	if err = p.(CloseCheckoutProvider).CloseCheckout(ctx, "ORD1", ""); err != nil {
		t.Fatal(err)
	}
	_, err = p.Refund(ctx, RefundParams{OrderNo: "ORD1", RefundNo: "REF1", AmountCents: 50, Currency: "CNY"})
	if err != nil {
		t.Fatal(err)
	}
	refund, err := p.(RefundQueryProvider).QueryRefund(ctx, RefundQueryParams{RefundNo: "REF1", OrderNo: "ORD1", ExpectedAmountCents: 50, ExpectedCurrency: "CNY"})
	if err != nil || refund.Status != StatusSucceeded {
		t.Fatalf("refund query %+v %v", refund, err)
	}
	if calls.Load() != 5 {
		t.Fatalf("unexpected lifecycle count %d", calls.Load())
	}
}
func TestPayPalRegistryApprovalDoesNotCaptureBeforeDurableClaim(t *testing.T) {
	var captures atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth2/token":
			fmt.Fprint(w, `{"access_token":"token","expires_in":300}`)
		case "/v1/notifications/verify-webhook-signature":
			fmt.Fprint(w, `{"verification_status":"SUCCESS"}`)
		default:
			captures.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	reg := &Registry{BaseURL: "https://panel.example.test", Client: &http.Client{Transport: registryTransport{target: target, next: http.DefaultTransport}, Timeout: time.Second}}
	m := Merchant{ID: 1, Provider: ProviderPayPal, Environment: "sandbox", AccountID: "merchant1", Name: "PayPal", Entrypoint: "hosted", Currencies: []string{"USD"}, Secrets: MerchantSecrets{ClientID: "id", ClientSecret: "secret", WebhookID: "hook"}}
	p, err := reg.Build(m)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	for _, key := range []string{"Paypal-Auth-Algo", "Paypal-Cert-Url", "Paypal-Transmission-Id", "Paypal-Transmission-Sig", "Paypal-Transmission-Time"} {
		header.Set(key, "test")
	}
	body := []byte(`{"id":"EVENT1","event_type":"CHECKOUT.ORDER.APPROVED","resource":{"id":"PAYPAL1","status":"APPROVED","purchase_units":[{"custom_id":"7","reference_id":"ORD1","payee":{"merchant_id":"merchant1"},"amount":{"currency_code":"USD","value":"1.00"}}]}}`)
	event, err := p.ParseAndVerifyWebhook(context.Background(), header, body)
	if err != nil || event.Status != StatusPending || captures.Load() != 0 {
		t.Fatalf("pre-claim capture or malformed approval %+v captures%d err%v", event, captures.Load(), err)
	}
	var value map[string]any
	if json.Unmarshal(body, &value) != nil {
		t.Fatal("bad fixture")
	}
	bad := strings.ReplaceAll(string(body), "merchant1", "otherMerchant")
	if _, err = p.ParseAndVerifyWebhook(context.Background(), header, []byte(bad)); !errors.Is(err, ErrMerchantMismatch) {
		t.Fatalf("wrong merchant accepted: %v", err)
	}
}

func TestWeChatRegistrySignedLifecycleContract(t *testing.T) {
	key, private, public := contractKeys(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		params := map[string]string{}
		authorization := strings.TrimPrefix(r.Header.Get("Authorization"), "WECHATPAY2-SHA256-RSA2048 ")
		for _, part := range strings.Split(authorization, ",") {
			pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(pair) == 2 {
				params[pair[0]] = strings.Trim(pair[1], `"`)
			}
		}
		canonical := r.Method + "\n" + r.URL.RequestURI() + "\n" + params["timestamp"] + "\n" + params["nonce_str"] + "\n" + string(raw) + "\n"
		if VerifyRSASHA256(canonical, params["signature"], &key.PublicKey) != nil {
			t.Error("wechat request signature invalid")
		}
		body := `{"code_url":"weixin://wxpay/bizpayurl?pr=local"}`
		switch {
		case strings.Contains(r.URL.Path, "/close"):
			body = ""
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/refund/"):
			body = `{"refund_id":"RF1","status":"SUCCESS","transaction_id":"TX1","amount":{"refund":50,"currency":"CNY"}}`
		case strings.Contains(r.URL.Path, "/refund/"):
			body = `{"refund_id":"RF1","status":"SUCCESS","transaction_id":"TX1","amount":{"refund":50,"currency":"CNY"}}`
		case r.Method == http.MethodGet:
			body = `{"mchid":"mch1","appid":"app1","transaction_id":"TX1","trade_state":"SUCCESS","amount":{"total":100,"currency":"CNY"}}`
		default:
			if !strings.Contains(string(raw), `"notify_url":"https://panel.example.test/api/v1/payments/1/webhook"`) {
				t.Error("Native notify_url missing")
			}
		}
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		signature, err := SignRSASHA256(timestamp+"\nreplynonce\n"+body+"\n", key)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Wechatpay-Timestamp", timestamp)
		w.Header().Set("Wechatpay-Nonce", "replynonce")
		w.Header().Set("Wechatpay-Serial", "provider-key")
		w.Header().Set("Wechatpay-Signature", signature)
		if body == "" {
			w.WriteHeader(http.StatusNoContent)
		} else {
			fmt.Fprint(w, body)
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	reg := &Registry{BaseURL: "https://panel.example.test", Client: &http.Client{Transport: registryTransport{target: target, next: http.DefaultTransport}, Timeout: time.Second}}
	m := Merchant{ID: 1, Provider: ProviderWeChat, Environment: "live", AccountID: "mch1", Name: "WeChat", Currencies: []string{"CNY"}, Entrypoint: "native", Secrets: MerchantSecrets{AppID: "app1", APIv3Key: strings.Repeat("v", 32), PrivateKeyPEM: private, ProviderPublicKeyPEM: public, ProviderKeyID: "provider-key", CertificateSerial: "merchant-serial"}}
	p, err := reg.Build(m)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	result, err := p.CreateCheckout(ctx, CheckoutParams{OrderID: 7, OrderNo: "ORD1", AmountCents: 100, Currency: "CNY"})
	if err != nil || ValidateCheckoutDestination(ProviderWeChat, result) != nil {
		t.Fatalf("checkout %+v %v", result, err)
	}
	status, err := p.QueryPayment(ctx, "ORD1", "")
	if err != nil || status.Status != StatusSucceeded || status.AmountCents != 100 {
		t.Fatalf("query %+v %v", status, err)
	}
	if err = p.(CloseCheckoutProvider).CloseCheckout(ctx, "ORD1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Refund(ctx, RefundParams{OrderNo: "ORD1", RefundNo: "REF1", AmountCents: 50, OriginalAmountCents: 100, Currency: "CNY"}); err != nil {
		t.Fatal(err)
	}
	refund, err := p.(RefundQueryProvider).QueryRefund(ctx, RefundQueryParams{RefundNo: "REF1", ExpectedAmountCents: 50, ExpectedCurrency: "CNY"})
	if err != nil || refund.Status != StatusSucceeded {
		t.Fatalf("refund query %+v %v", refund, err)
	}
	if calls.Load() != 5 {
		t.Fatalf("unexpected lifecycle request count%d", calls.Load())
	}
}
func TestRegistryRejectsUnsignedProviderFactsAndUnsafeDestinations(t *testing.T) {
	_, private, public := contractKeys(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"alipay_trade_query_response":{"code":"10000","trade_status":"TRADE_SUCCESS","total_amount":"1.00"}}`)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	r := &Registry{BaseURL: "https://panel.example.test", Client: &http.Client{Transport: registryTransport{target: target, next: http.DefaultTransport}}}
	m := Merchant{ID: 1, Provider: ProviderAlipay, Environment: "sandbox", AccountID: "seller1", Name: "Ali", Currencies: []string{"CNY"}, Entrypoint: "native", Secrets: MerchantSecrets{AppID: "app", PrivateKeyPEM: private, ProviderPublicKeyPEM: public}}
	p, err := r.Build(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.QueryPayment(context.Background(), "ORD1", ""); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("unsigned result accepted: %v", err)
	}
	for _, provider := range []string{ProviderStripe, ProviderPayPal, ProviderAlipay, ProviderWeChat} {
		if ValidateCheckoutDestination(provider, CheckoutResult{SessionID: "session", URL: "https://evil.example/redirect"}) == nil {
			t.Fatalf("untrusted redirect accepted for %s", provider)
		}
	}
	if ValidateMerchantEvent(Merchant{Provider: ProviderStripe, Environment: "live", AccountID: "acct_one"}, []byte(`{"livemode":false}`), WebhookEvent{Provider: ProviderStripe}) == nil {
		t.Fatal("cross-environment Stripe webhook accepted")
	}
}

func TestRefundQueryRecoversStableReferencesAndChecksFacts(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.URL.Path != "/v1/oauth2/token" {
			writes.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/v1/refunds":
			if r.URL.Query().Get("payment_intent") != "pi_original" {
				t.Error("refund search not bound to original PI")
			}
			fmt.Fprint(w, `{"data":[{"id":"re_existing","metadata":{"refund_no":"REF_APPROVED"}}],"has_more":false}`)
		case "/v1/refunds/re_existing":
			fmt.Fprint(w, `{"id":"re_existing","status":"succeeded","amount":50,"currency":"usd","payment_intent":"pi_original"}`)
		case "/v1/oauth2/token":
			fmt.Fprint(w, `{"access_token":"token","expires_in":300}`)
		case "/v2/checkout/orders/ORDER_ORIGINAL":
			fmt.Fprint(w, `{"purchase_units":[{"payments":{"refunds":[{"id":"PAYPAL_REFUND","invoice_id":"REF_APPROVED"}]}}]}`)
		case "/v2/payments/refunds/PAYPAL_REFUND":
			fmt.Fprint(w, `{"id":"PAYPAL_REFUND","invoice_id":"REF_APPROVED","status":"COMPLETED","amount":{"value":"0.50","currency_code":"USD"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	ctx := context.Background()
	stripe := NewStripe("unused", "unused")
	stripe.Base = server.URL
	stripe.Client = server.Client()
	params := RefundQueryParams{RefundNo: "REF_APPROVED", ProviderTxnID: "pi_original", ExpectedAmountCents: 50, ExpectedCurrency: "USD"}
	result, err := stripe.QueryRefund(ctx, params)
	if err != nil || result.AmountCents != 50 || result.Currency != "USD" || result.ProviderRefundID != "re_existing" {
		t.Fatalf("Stripe refund recovery %+v %v", result, err)
	}
	params.ExpectedAmountCents = 51
	if result, err = stripe.QueryRefund(ctx, params); !errors.Is(err, ErrAmountMismatch) || result.AmountCents != 50 {
		t.Fatalf("mismatched refund approved: %+v %v", result, err)
	}
	paypal := NewPayPal("client", "secret", "hook")
	paypal.Base = server.URL
	paypal.Client = server.Client()
	result, err = paypal.QueryRefund(ctx, RefundQueryParams{RefundNo: "REF_APPROVED", SessionID: "ORDER_ORIGINAL", ExpectedAmountCents: 50, ExpectedCurrency: "USD"})
	if err != nil || result.AmountCents != 50 || result.Currency != "USD" || result.ProviderRefundID != "PAYPAL_REFUND" {
		t.Fatalf("PayPal refund recovery %+v %v", result, err)
	}
	if _, err = paypal.QueryRefund(ctx, RefundQueryParams{RefundNo: "UNKNOWN", ProviderTxnID: "CAPTURE_ONLY"}); !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("missing lookup path fabricated: %v", err)
	}
	if writes.Load() != 0 {
		t.Fatal("refund recovery initiated a new payout")
	}
	if NormalizeRefundStatus("ABNORMAL") != "uncertain" {
		t.Fatal("ambiguous WeChat refund released budget")
	}
}
