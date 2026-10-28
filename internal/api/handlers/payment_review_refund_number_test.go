package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// PD-01 regression: a tenant pre-occupying the deterministic review refund
// number must produce a loud conflict, never a silent cross-tenant refund
// object substitution; removing the squatter restores normal creation.
func TestReviewRefundRefusesForeignRefundNumber(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()

	seedTenant := func(name string) (int64, int64, int64) {
		t.Helper()
		var brand, tenant, order, payment int64
		if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES($1,$1) RETURNING id", name).Scan(&brand); err != nil {
			t.Fatal(err)
		}
		if err := platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,$2,$3,'active') RETURNING id", brand, name, name).Scan(&tenant); err != nil {
			t.Fatal(err)
		}
		if err := platform.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency) VALUES($1,$2,'new','paid',10000,'CNY') RETURNING id",
			tenant, "ord-"+name).Scan(&order); err != nil {
			t.Fatal(err)
		}
		if err := platform.QueryRow(ctx, "INSERT INTO payments(tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status) VALUES($1,$2,'stripe',$3,10000,'CNY','succeeded') RETURNING id",
			tenant, order, "txn-"+name).Scan(&payment); err != nil {
			t.Fatal(err)
		}
		return tenant, order, payment
	}
	tenantA, orderA, paymentA := seedTenant("refund-a")
	tenantB, orderB, paymentB := seedTenant("refund-b")

	var operator int64
	if err := platform.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('operator@example.test','fixture-only') RETURNING id").Scan(&operator); err != nil {
		t.Fatal(err)
	}

	var reviewB int64
	if err := platform.QueryRow(ctx, "INSERT INTO payment_manual_reviews(tenant_id,order_id,payment_id,reason) VALUES($1,$2,$3,'verified cash needs manual refund') RETURNING id",
		tenantB, orderB, paymentB).Scan(&reviewB); err != nil {
		t.Fatal(err)
	}

	// Tenant A squats the deterministic review refund number of review B.
	var squatterID int64
	if err := platform.QueryRow(ctx, "INSERT INTO refunds(tenant_id,order_id,payment_id,refund_no,amount_cents,currency,status) VALUES($1,$2,$3,$4,1,'CNY','pending') RETURNING id",
		tenantA, orderA, paymentA, "REVIEW-"+strconv.FormatInt(reviewB, 10)).Scan(&squatterID); err != nil {
		t.Fatal(err)
	}

	if _, err := platform.Exec(ctx, "INSERT INTO platform_admins(user_id) VALUES($1)", operator); err != nil {
		t.Fatal(err)
	}
	h := &PaymentReviewHandler{Pool: platform}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1/platform")
	group.Use(func(c *gin.Context) {
		c.Set(middleware.ContextUserID, operator)
		c.Next()
	})
	group.POST("/payment-reviews/:id/resolve", h.Resolve)

	resolve := func() *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost,
			"/api/v1/platform/payment-reviews/"+strconv.FormatInt(reviewB, 10)+"/resolve",
			strings.NewReader(`{"action":"request_refund","reason":"audited operator refund decision","preview_digest":"`+financialPreviewForTest(t, platform, operator, reviewB, "request_refund", "audited operator refund decision")+`"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w
	}

	w := resolve()
	if w.Code != http.StatusConflict {
		t.Fatalf("squatted refund number accepted: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "PAYMENT_REVIEW_REFUND_NUMBER_TAKEN") {
		t.Fatalf("missing loud conflict code: %s", w.Body.String())
	}
	var refundTenant, refundOrder int64
	var count int
	if err := platform.QueryRow(ctx, "SELECT tenant_id,order_id FROM refunds WHERE id=$1", squatterID).Scan(&refundTenant, &refundOrder); err != nil {
		t.Fatal(err)
	}
	if refundTenant != tenantA || refundOrder != orderA {
		t.Fatal("squatter row was modified")
	}
	if err := platform.QueryRow(ctx, "SELECT count(*) FROM refunds r JOIN payments p ON p.id=r.payment_id WHERE p.tenant_id=$1", tenantB).Scan(&count); err != nil || count != 0 {
		t.Fatalf("tenant B gained a refund row: count=%d err=%v", count, err)
	}

	// refunds is append-only (REFUND_IMMUTABLE): the squatter cannot be
	// removed here. The positive path is covered by the dedicated subtest.
}

// The positive path: without a squatter, the review refund is created for the
// correct tenant with the deterministic number.
func TestReviewRefundCreatedForOwningTenant(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()

	var brand, tenant, order, payment, operator int64
	if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('ok','OK') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'OK','ok','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('op2@example.test','fixture-only') RETURNING id").Scan(&operator); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency) VALUES($1,'ord-ok','new','paid',10000,'CNY') RETURNING id", tenant).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO payments(tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status) VALUES($1,$2,'stripe','txn-ok',10000,'CNY','succeeded') RETURNING id", tenant, order).Scan(&payment); err != nil {
		t.Fatal(err)
	}
	var reviewB int64
	if err := platform.QueryRow(ctx, "INSERT INTO payment_manual_reviews(tenant_id,order_id,payment_id,reason) VALUES($1,$2,$3,'verified cash needs manual refund') RETURNING id",
		tenant, order, payment).Scan(&reviewB); err != nil {
		t.Fatal(err)
	}

	if _, err := platform.Exec(ctx, "INSERT INTO platform_admins(user_id) VALUES($1)", operator); err != nil {
		t.Fatal(err)
	}
	h := &PaymentReviewHandler{Pool: platform}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1/platform")
	group.Use(func(c *gin.Context) {
		c.Set(middleware.ContextUserID, operator)
		c.Next()
	})
	group.POST("/payment-reviews/:id/resolve", h.Resolve)

	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost,
		"/api/v1/platform/payment-reviews/"+strconv.FormatInt(reviewB, 10)+"/resolve",
		strings.NewReader(`{"action":"request_refund","reason":"audited operator refund decision","preview_digest":"`+financialPreviewForTest(t, platform, operator, reviewB, "request_refund", "audited operator refund decision")+`"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("refund creation failed: %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := platform.QueryRow(ctx, "SELECT count(*) FROM refunds WHERE refund_no=$1 AND tenant_id=$2 AND order_id=$3 AND status='pending'",
		"REVIEW-"+strconv.FormatInt(reviewB, 10), tenant, order).Scan(&count); err != nil || count != 1 {
		t.Fatalf("tenant refund missing: count=%d err=%v", count, err)
	}
	// A repeated resolve stays idempotent: still exactly one pending refund
	// for this review.
	w = httptest.NewRecorder()
	req = httptest.NewRequestWithContext(ctx, http.MethodPost,
		"/api/v1/platform/payment-reviews/"+strconv.FormatInt(reviewB, 10)+"/resolve",
		strings.NewReader(`{"action":"request_refund","reason":"audited operator refund decision","preview_digest":"`+financialPreviewForTest(t, platform, operator, reviewB, "request_refund", "audited operator refund decision")+`"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if err := platform.QueryRow(ctx, "SELECT count(*) FROM refunds WHERE refund_no=$1 AND tenant_id=$2",
		"REVIEW-"+strconv.FormatInt(reviewB, 10), tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("repeated resolve duplicated refund: count=%d err=%v", count, err)
	}
}
