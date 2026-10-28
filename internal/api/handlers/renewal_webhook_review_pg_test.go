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

	"github.com/bushixiucai/urmail/internal/store"
)

func TestRecurringWebhookIndependentCancellationAndMissingIntentPG(t *testing.T) {
	for _, mode := range []string{"cancel_during_provider_get", "missing_committed_intent"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecurringFixture(t)
			f.enroll(t)
			worker := f.worker()
			if e := worker.RunOnce(t.Context()); e != nil {
				t.Fatal(e)
			}
			var key, currency string
			var amount, attempt int64
			var before time.Time
			if e := f.s.p.QueryRow(t.Context(), "SELECT id,dedupe_key,amount_cents,currency FROM subscription_renewal_attempts WHERE tenant_id=$1 AND side_effect_intent_at IS NOT NULL", f.s.tid).Scan(&attempt, &key, &amount, &currency); e != nil {
				t.Fatal(e)
			}
			if e := f.s.p.QueryRow(t.Context(), "SELECT period_end FROM subscriptions WHERE tenant_id=$1 AND id=$2", f.s.tid, f.subscription).Scan(&before); e != nil {
				t.Fatal(e)
			}
			if mode == "missing_committed_intent" {
				key = "provider-confirmed-but-local-intent-missing"
			}
			txn := "pi_independent_review"
			metadata := map[string]string{"idempotency_key": key, "tenant_id": fmt.Sprint(f.s.tid), "subscription_id": fmt.Sprint(f.subscription)}
			intent := map[string]any{"id": txn, "object": "payment_intent", "status": "succeeded", "livemode": false, "amount": amount, "amount_received": amount, "currency": strings.ToLower(currency), "metadata": metadata}
			var posts, reads atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					posts.Add(1)
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if r.URL.Path == "/v1/account" {
					_, _ = fmt.Fprint(w, `{"id":"acct_recurringfixture"}`)
					return
				}
				if r.URL.Path != "/v1/payment_intents/"+txn {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				reads.Add(1)
				// At this point the handler has already read the active local intent.
				// Change the owned DB during the actual provider GET, before returning funds.
				if mode == "cancel_during_provider_get" {
					if _, e := f.s.p.Exec(t.Context(), "UPDATE subscription_renewal_attempts SET state='canceled' WHERE tenant_id=$1 AND id=$2", f.s.tid, attempt); e != nil {
						t.Error(e)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
				}
				_ = json.NewEncoder(w).Encode(intent)
			}))
			defer upstream.Close()
			target, _ := url.Parse(upstream.URL)
			f.d.Registry.Client = &http.Client{Transport: merchantHTTPTransport{target: target}, Timeout: time.Second}
			h := &PaymentGatewayHandler{Registry: f.d.Registry, Platform: store.New(f.s.p), Pool: f.s.p}
			router := gin.New()
			router.POST("/payments/:merchant_id/webhook", h.Webhook)
			eventID := "evt_independent_" + mode
			payload, _ := json.Marshal(map[string]any{"id": eventID, "type": "payment_intent.succeeded", "livemode": false, "account": "acct_recurringfixture", "data": map[string]any{"object": intent}})
			for pass := 0; pass < 2; pass++ {
				stamp := strconv.FormatInt(time.Now().Unix(), 10)
				mac := hmac.New(sha256.New, []byte("whsec_fixture"))
				_, _ = mac.Write([]byte(stamp + "." + string(payload)))
				request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, fmt.Sprintf("/payments/%d/webhook", f.merchant), strings.NewReader(string(payload)))
				request.Header.Set("Stripe-Signature", "t="+stamp+",v1="+hex.EncodeToString(mac.Sum(nil)))
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, request)
				if rec.Code != http.StatusServiceUnavailable {
					t.Fatalf("unresolved signed receipt pass%d became terminal: %d %s", pass, rec.Code, rec.Body.String())
				}
			}
			var state string
			if e := f.s.p.QueryRow(t.Context(), "SELECT state FROM payment_webhook_inbox WHERE merchant_id=$1 AND event_id=$2", f.merchant, eventID).Scan(&state); e != nil || state != "uncertain" {
				t.Fatal("manual review receipt lost", state, e)
			}
			var payments int
			var after time.Time
			if e := f.s.p.QueryRow(t.Context(), "SELECT count(*) FROM payments WHERE provider_txn_id=$1", txn).Scan(&payments); e != nil || payments != 0 || posts.Load() != 0 {
				t.Fatal("unresolved funds created credit or POST", payments, posts.Load(), e)
			}
			if e := f.s.p.QueryRow(t.Context(), "SELECT period_end FROM subscriptions WHERE tenant_id=$1 AND id=$2", f.s.tid, f.subscription).Scan(&after); e != nil || !after.Equal(before) {
				t.Fatal("unresolved receipt extended entitlement", before, after, e)
			}
			if mode == "cancel_during_provider_get" && reads.Load() != 1 {
				t.Fatal("actual in-flight GET cancellation hook not exercised", reads.Load())
			}
		})
	}
}
