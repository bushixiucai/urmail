package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/billingrenew"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestRecurringMerchantWebhookQueriesBeforeOneSettlementPG(t *testing.T) {
	f := newRecurringFixture(t)
	f.enroll(t)
	worker := f.worker()
	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var key, currency string
	var amount int64
	if err := f.s.p.QueryRow(t.Context(), "SELECT dedupe_key,amount_cents,currency FROM subscription_renewal_attempts WHERE tenant_id=$1 AND side_effect_intent_at IS NOT NULL", f.s.tid).Scan(&key, &amount, &currency); err != nil {
		t.Fatal(err)
	}
	var unavailable atomic.Bool
	unavailable.Store(true)
	var writes atomic.Int32
	metadata := map[string]string{"idempotency_key": key, "tenant_id": fmt.Sprint(f.s.tid), "subscription_id": fmt.Sprint(f.subscription)}
	intent := map[string]any{"id": "pi_recurring_webhook", "object": "payment_intent", "status": "succeeded", "livemode": false, "amount": amount, "amount_received": amount, "currency": strings.ToLower(currency), "metadata": metadata}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/v1/account" {
			_, _ = fmt.Fprint(w, `{"id":"acct_recurringfixture"}`)
			return
		}
		if r.URL.Path != "/v1/payment_intents/pi_recurring_webhook" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(intent)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	f.d.Registry.Client = &http.Client{Transport: merchantHTTPTransport{target: target}, Timeout: time.Second}
	h := &PaymentGatewayHandler{Registry: f.d.Registry, Platform: store.New(f.s.p), Pool: f.s.p}
	router := gin.New()
	router.POST("/payments/:merchant_id/webhook", h.Webhook)
	payload, _ := json.Marshal(map[string]any{"id": "evt_recurring_webhook", "type": "payment_intent.succeeded", "livemode": false, "account": "acct_recurringfixture", "data": map[string]any{"object": intent}})
	call := func() *httptest.ResponseRecorder {
		t.Helper()
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte("whsec_fixture"))
		_, _ = mac.Write([]byte(stamp + "." + string(payload)))
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, fmt.Sprintf("/payments/%d/webhook", f.merchant), strings.NewReader(string(payload)))
		request.Header.Set("Stripe-Signature", "t="+stamp+",v1="+hex.EncodeToString(mac.Sum(nil)))
		out := httptest.NewRecorder()
		router.ServeHTTP(out, request)
		return out
	}
	if out := call(); out.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfirmed funds acknowledged: %d %s", out.Code, out.Body)
	}
	unavailable.Store(false)
	for pass := 0; pass < 2; pass++ {
		if out := call(); out.Code != http.StatusOK {
			t.Fatalf("confirmed receipt pass%d: %d %s", pass, out.Code, out.Body)
		}
	}
	var count int
	if err := f.s.p.QueryRow(t.Context(), "SELECT count(*) FROM payments WHERE tenant_id=$1 AND provider_txn_id='pi_recurring_webhook' AND order_id IS NOT NULL", f.s.tid).Scan(&count); err != nil || count != 1 || writes.Load() != 0 {
		t.Fatalf("receipt replay changed money or used POST: count=%d writes=%d err=%v", count, writes.Load(), err)
	}
	if err := f.s.p.QueryRow(t.Context(), "SELECT count(*) FROM invoices i JOIN payments p ON p.id=i.source_payment_id WHERE p.provider_txn_id='pi_recurring_webhook' AND i.tenant_id=$1", f.s.tid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("renewal invoice not linked exactly once: %d %v", count, err)
	}
	q := store.New(f.s.p)
	const fenceEvent = "evt_recurring_fence"
	if _, err := q.PlatformRecordMerchantWebhook(t.Context(), store.PlatformRecordMerchantWebhookParams{MerchantID: f.merchant, EventID: fenceEvent, PayloadHash: strings.Repeat("a", 64), Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	params := store.PlatformClaimRecurringWebhookParams{MerchantID: f.merchant, EventID: fenceEvent, PayloadHash: strings.Repeat("a", 64)}
	old, err := q.PlatformClaimRecurringWebhook(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.p.Exec(t.Context(), "UPDATE payment_webhook_inbox SET processing_until=now()-interval '1 second' WHERE merchant_id=$1 AND event_id=$2", f.merchant, fenceEvent); err != nil {
		t.Fatal(err)
	}
	current, err := q.PlatformClaimRecurringWebhook(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	finish := store.PlatformFinishRecurringWebhookParams{MerchantID: f.merchant, EventID: fenceEvent, PayloadHash: params.PayloadHash, ProcessingUntil: old.ProcessingUntil, State: "completed", ResultCode: pgtype.Text{String: "settled", Valid: true}}
	if n, e := q.PlatformFinishRecurringWebhook(t.Context(), finish); e != nil || n != 0 {
		t.Fatalf("old callback lease overwrote new claim: %d %v", n, e)
	}
	finish.ProcessingUntil = current.ProcessingUntil
	if n, e := q.PlatformFinishRecurringWebhook(t.Context(), finish); e != nil || n != 1 {
		t.Fatalf("current claim could not finish: %d %v", n, e)
	}
}

func TestRegisteredStripeNeedsMerchantApprovalPG(t *testing.T) {
	f := newRecurringFixture(t)
	f.d.Adapters = billingrenew.NewProductionAdapters(f.d.Registry)
	if _, err := f.s.p.Exec(t.Context(), "UPDATE payment_recurring_capabilities SET adapter_key='',merchant_approved=false,runtime_available=false WHERE merchant_id=$1", f.merchant); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.Overview(t.Context(), f.s.tid)
	if err != nil || len(out.Capabilities) != 1 || !out.Capabilities[0].AdapterRegistered || out.Capabilities[0].Status != "unverified" || out.Capabilities[0].MerchantApproved {
		t.Fatalf("registration invented merchant approval: %+v %v", out.Capabilities, err)
	}
}
