package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func financialPreviewForTest(t *testing.T, p *db.Pool, actor, review int64, action, reason string) string {
	t.Helper()
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(middleware.ContextUserID, actor) })
	router.POST("/:id", (&PaymentReviewHandler{Pool: p}).Preview)
	w := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"action": action, "reason": reason})
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/"+strconv.FormatInt(review, 10), strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("preview %d %s", w.Code, w.Body)
	}
	var out struct {
		Digest string `json:"digest"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Digest) != 64 {
		t.Fatal(w.Body)
	}
	return out.Digest
}

func TestFinancialReconciliationAndPreviewPG(t *testing.T) {
	p, app := testdb.OpenMigrated(t)
	ctx := t.Context()
	gin.SetMode(gin.TestMode)
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var v int64
		if e := p.QueryRow(ctx, query, args...).Scan(&v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, e := p.Exec(ctx, query, args...); e != nil {
			t.Fatal(e)
		}
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('financial-proof','Financial') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Financial','financial-proof','active') RETURNING id", brand)
	foreign := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Foreign','financial-foreign','active') RETURNING id", brand)
	admin := scalar("INSERT INTO users(email,password_hash) VALUES('financial-admin@example.test','fixture') RETURNING id")
	reader := scalar("INSERT INTO users(email,password_hash) VALUES('financial-reader@example.test','fixture') RETURNING id")
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", admin)
	exec("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'billing')", reader, tenant)
	newReview := func(name string) (int64, int64) {
		t.Helper()
		order := scalar("INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency) VALUES($1,$2,'new','pending',10000,'CNY') RETURNING id", tenant, name)
		payment := scalar("INSERT INTO payments(tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status,raw) VALUES($1,$2,'stripe',$3,10000,'CNY','succeeded','{\"client_secret\":\"NEVER_EXPORT_PROVIDER_SECRET\"}') RETURNING id", tenant, order, "pi_"+name)
		review := scalar("INSERT INTO payment_manual_reviews(tenant_id,order_id,payment_id,reason) VALUES($1,$2,$3,'verified cash with an unresolved paid service') RETURNING id", tenant, order, payment)
		return order, review
	}
	order, review := newReview("financial-initial")
	scalar("INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency) VALUES($1,'FOREIGN-PRIVATE','new','pending',12,'CNY') RETURNING id", foreign)
	from := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	to := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	serve := func(pool *db.Pool, platform bool, actor int64, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(middleware.ContextUserID, actor)
			c.Set(middleware.ContextTenantID, tenant)
		})
		h := &FinancialReconciliation{Pool: pool, Platform: platform}
		r.GET("/report", h.Report)
		r.GET("/report/export", h.Report)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))
		return w
	}
	query := fmt.Sprintf("?from=%s&to=%s", from, to)
	for _, platform := range []bool{false, true} {
		pool, actor, path := app, reader, "/report/export"+query
		if platform {
			pool, actor, path = p, admin, path+"&tenant_id="+strconv.FormatInt(tenant, 10)
		}
		w := serve(pool, platform, actor, path)
		if w.Code != 200 || strings.Contains(w.Body.String(), "NEVER_EXPORT_PROVIDER_SECRET") || strings.Contains(w.Body.String(), "FOREIGN-PRIVATE") || !strings.Contains(w.Body.String(), "manual_review_open") {
			t.Fatalf("scoped export platform=%t %d %s", platform, w.Code, w.Body)
		}
		var report struct {
			Totals map[string]financialTotal `json:"totals"`
		}
		if json.Unmarshal(w.Body.Bytes(), &report) != nil || report.Totals["CNY"].Confirmed != "10000" {
			t.Fatal(w.Body)
		}
	}
	if w := serve(app, false, reader, "/report"+query+"&tenant_id="+strconv.FormatInt(foreign, 10)); w.Code != 403 {
		t.Fatalf("cross tenant %d", w.Code)
	}
	if w := serve(p, true, reader, "/report"+query+"&tenant_id="+strconv.FormatInt(tenant, 10)); w.Code != 403 {
		t.Fatalf("non-platform admin %d", w.Code)
	}
	reason := "Reviewed actual receipt and selected refund approval"
	resolve := func(id, actor int64, digest, action string) *httptest.ResponseRecorder {
		t.Helper()
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(middleware.ContextUserID, actor) })
		r.POST("/:id", (&PaymentReviewHandler{Pool: p}).Resolve)
		body, _ := json.Marshal(map[string]string{"action": action, "reason": reason, "preview_digest": digest})
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/"+strconv.FormatInt(id, 10), strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	digest := financialPreviewForTest(t, p, admin, review, "request_refund", reason)
	if w := resolve(review, admin, digest, "fulfill_after_prepaid"); w.Code != 409 {
		t.Fatalf("cross-action proof %d %s", w.Code, w.Body)
	}
	exec("UPDATE orders SET status='canceled' WHERE id=$1", order)
	if w := resolve(review, admin, digest, "request_refund"); w.Code != 409 {
		t.Fatalf("changed facts %d %s", w.Code, w.Body)
	}
	for _, mode := range []string{"restore", "revoked_actor", "changed_refund", "expired", "valid"} {
		t.Run(mode, func(t *testing.T) {
			_, id := newReview("financial-" + mode)
			proof := financialPreviewForTest(t, p, admin, id, "request_refund", reason)
			switch mode {
			case "restore":
				exec("INSERT INTO settings(scope,scope_id,key,value) VALUES('platform',0,'restore_replay_review','{\"sync_epoch\":2}') ON CONFLICT(scope,scope_id,key) DO UPDATE SET value='{" + "\"sync_epoch\":3}'")
			case "revoked_actor":
				exec("DELETE FROM platform_admins WHERE user_id=$1", admin)
			case "changed_refund":
				exec("INSERT INTO refunds(tenant_id,order_id,payment_id,refund_no,amount_cents,currency,status) SELECT tenant_id,order_id,payment_id,'OTHER-FINANCIAL-REFUND',1,'CNY','pending' FROM payment_manual_reviews WHERE id=$1", id)
			case "expired":
				// Append an actually expired proof without mutating immutable history.
				expired := strings.Repeat("0", 64)
				exec("INSERT INTO audit_logs(tenant_id,actor_user_id,action,target_type,target_id,detail,created_at) SELECT tenant_id,actor_user_id,action,target_type,target_id,jsonb_set(detail,'{digest}',to_jsonb($2::text)),now()-interval '11 minutes' FROM audit_logs WHERE action='PREVIEW receipt.review' AND detail->>'digest'=$1", proof, expired)
				proof = expired
			}
			w := resolve(id, admin, proof, "request_refund")
			if mode == "valid" {
				if w.Code != 202 {
					t.Fatalf("valid preview %d %s", w.Code, w.Body)
				}
				if second := resolve(id, admin, proof, "request_refund"); second.Code != 409 {
					t.Fatalf("changed replay %d", second.Code)
				}
				fresh := financialPreviewForTest(t, p, admin, id, "request_refund", reason)
				if retry := resolve(id, admin, fresh, "request_refund"); retry.Code != 202 {
					t.Fatalf("fresh preview idempotent refund %d %s", retry.Code, retry.Body)
				}
			} else if w.Code != 409 && w.Code != 403 {
				t.Fatalf("stale proof %s %d %s", mode, w.Code, w.Body)
			}
			if mode == "revoked_actor" {
				exec("INSERT INTO platform_admins(user_id) VALUES($1)", admin)
			}
		})
	}
	if n := scalar("SELECT count(*) FROM audit_logs WHERE action IN('EXPORT financial.reconciliation','GET /api/v1/billing/reconciliation/export')"); n != 2 {
		t.Fatal("missing export audit", n)
	}
	exec("CREATE FUNCTION reject_financial_export_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='EXPORT financial.reconciliation' THEN RAISE EXCEPTION 'controlled failure'; END IF; RETURN NEW; END $$")
	exec("CREATE TRIGGER reject_financial_export_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_financial_export_audit()")
	if w := serve(p, true, admin, "/report/export"+query+"&tenant_id="+strconv.FormatInt(tenant, 10)); w.Code != 500 || strings.Contains(w.Body.String(), "financial-initial") {
		t.Fatal("export escaped failed audit", w.Code, w.Body)
	}
	exec("DROP TRIGGER reject_financial_export_audit ON audit_logs; DROP FUNCTION reject_financial_export_audit()")
	// A captured receipt remains gross cash after its status becomes refunded;
	// pending refunds are not deducted. Persist the actual source statuses.
	refundedOrder := scalar("INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency) VALUES($1,'financial-captured-refunded','new','refunded',10000,'CNY') RETURNING id", tenant)
	captured := scalar("INSERT INTO payments(tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status) VALUES($1,$2,'stripe','pi_actual_refunded',10000,'CNY','refunded') RETURNING id", tenant, refundedOrder)
	exec("INSERT INTO refunds(tenant_id,order_id,payment_id,refund_no,amount_cents,currency,status,approved_by,approval_reason,approved_at,completed_at) VALUES($1,$2,$3,'financial-completed-refund',10000,'CNY','completed',$4,'Independently verified refund receipt',now(),now())", tenant, refundedOrder, captured, admin)
	w := serve(p, true, admin, "/report/export"+query+"&tenant_id="+strconv.FormatInt(tenant, 10))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var out struct {
		Items []financialItem `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	for _, item := range out.Items {
		var f financialFact
		_ = json.Unmarshal(item.Facts, &f)
		if f.ID == refundedOrder {
			_, totals, e := summarizeFinancial([][]byte{item.Facts})
			if e != nil || totals["CNY"].Confirmed != "10000" || totals["CNY"].Refunded != "10000" || totals["CNY"].Net != "0" {
				t.Fatalf("refunded gross/net %v %v", totals, e)
			}
		}
	}
	// Known bad frozen cycles must be explicit discrepancies, not merely raw JSON.
	_, wrongReview := newReview("financial-wrong-cycle")
	wrongOrder := scalar("SELECT order_id FROM payment_manual_reviews WHERE id=$1", wrongReview)
	exec("UPDATE orders SET status='paid',billing_cycle='monthly' WHERE id=$1", wrongOrder)
	calendar := `{"timezone":"UTC","anchor_at":"2030-01-15T00:00:00Z","version":1,"reanchor":false,"provisional":false}`
	exec("INSERT INTO subscription_terms(order_id,tenant_id,period_start,billing_period_start,period_end,plan_snapshot,base_amount_cents,currency,billing_cycle,calendar_snapshot) VALUES($1,$2,'2030-01-15','2030-01-15','2030-02-15','{}',10000,'USD','quarterly',$3)", wrongOrder, tenant, calendar)
	validOrder, _ := newReview("financial-valid-cycle")
	exec("UPDATE orders SET status='paid',billing_cycle='monthly' WHERE id=$1", validOrder)
	exec("INSERT INTO subscription_terms(order_id,tenant_id,period_start,billing_period_start,period_end,plan_snapshot,base_amount_cents,currency,billing_cycle,calendar_snapshot) VALUES($1,$2,'2031-01-15','2031-01-15','2031-02-15','{}',10000,'CNY','monthly',$3)", validOrder, tenant, strings.ReplaceAll(calendar, "2030", "2031"))
	completedRefund := scalar("SELECT id FROM refunds WHERE refund_no='financial-completed-refund'")
	exec("INSERT INTO subscription_terms(order_id,tenant_id,period_start,billing_period_start,period_end,plan_snapshot,base_amount_cents,currency,billing_cycle,calendar_snapshot,revoked_at,revoked_by_refund) VALUES($1,$2,'2032-01-15','2032-01-15','2032-02-15','{}',10000,'CNY','monthly',$3,now(),$4)", refundedOrder, tenant, strings.ReplaceAll(calendar, "2030", "2032"), completedRefund)
	highOrder := scalar("INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency) VALUES($1,'financial-exact-large','new','paid',9007199254740993,'CNY') RETURNING id", tenant)
	exec("INSERT INTO payments(tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status) VALUES($1,$2,'stripe','pi_exact_large',9007199254740993,'CNY','succeeded')", tenant, highOrder)
	w = serve(p, true, admin, "/report/export"+query+"&tenant_id="+strconv.FormatInt(tenant, 10))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"amount_cents_exact":"9007199254740993"`) {
		t.Fatal("exact large amount lost")
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	seenBad, seenValid, seenRevocation := false, false, false
	for _, item := range out.Items {
		var f financialFact
		_ = json.Unmarshal(item.Facts, &f)
		joined := strings.Join(item.Discrepancies, ",")
		if f.ID == wrongOrder {
			seenBad = strings.Contains(joined, "term_currency_mismatch") && strings.Contains(joined, "term_billing_cycle_mismatch") && strings.Contains(joined, "term_period_mismatch")
		}
		if f.ID == validOrder {
			seenValid = !strings.Contains(joined, "term_period_mismatch") && !strings.Contains(joined, "term_calendar_mismatch") && !strings.Contains(joined, "term_billing_cycle_mismatch")
		}
		if f.ID == refundedOrder {
			seenRevocation = strings.Contains(joined, "term_refund_revocation_mismatch")
		}
	}
	if !seenBad || !seenValid || !seenRevocation {
		t.Fatal("term discrepancies missing or falsely reported", seenBad, seenValid, seenRevocation)
	}
	// Never silently export a partial period after the strict row budget.
	exec("INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency) SELECT $1,'financial-budget-'||n,'new','pending',1,'CNY' FROM generate_series(1,1001) n", tenant)
	if limited := serve(p, true, admin, "/report/export"+query+"&tenant_id="+strconv.FormatInt(tenant, 10)); limited.Code != 409 {
		t.Fatal("export silently truncated", limited.Code)
	}
	if page := serve(app, false, reader, "/report"+query); page.Code != 200 || !strings.Contains(page.Body.String(), `"next_cursor":`) {
		t.Fatal("tenant pagination failed", page.Code, page.Body)
	}

}
