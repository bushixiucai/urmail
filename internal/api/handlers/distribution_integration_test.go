package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestAttributionCheckoutSettlementCommissionIsAtomicAndIdempotent(t *testing.T) {
	p, app := authIntegrationDatabase(t)
	ctx := context.Background()
	var brand, tid, uid, channel int64
	for _, step := range []struct {
		sql    string
		args   []any
		target *int64
	}{
		{"INSERT INTO brands(slug,name) VALUES('distribution','Distribution') RETURNING id", nil, &brand},
		{"INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Distribution','distribution','active') RETURNING id", []any{&brand}, &tid},
		{"INSERT INTO users(email,password_hash) VALUES('distribution@example.test','x') RETURNING id", nil, &uid},
		{"INSERT INTO channels(code,name,commission_bps) VALUES('distribution','Distribution',1000) RETURNING id", nil, &channel},
	} {
		args := []any{}
		for _, arg := range step.args {
			if ptr, ok := arg.(*int64); ok {
				args = append(args, *ptr)
			} else {
				args = append(args, arg)
			}
		}
		if err := p.QueryRow(ctx, step.sql, args...).Scan(step.target); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", uid, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO plans(code,name,price_cents,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('distribution','{}',10000,1073741824,5,2,100)"); err != nil {
		t.Fatal(err)
	}
	q := store.New(p.Pool)
	if _, err := q.CreateChannelAttribution(ctx, store.CreateChannelAttributionParams{TenantID: tid, ChannelID: channel, RefCode: pgtype.Text{String: "campaign", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	seedVerifiedBillingDomain(t, p, tid)
	orderHandler := NewBillingHandler(store.New(app.Pool))
	orderHandler.Pool = p
	orderHandler.QuoteHMACKey = []byte("integration-quote-hmac-key-that-is-at-least-32-bytes-long")
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tid); c.Set(middleware.ContextUserID, uid) }, middleware.RequireTenantTx(app, zap.NewNop()))
	engine.POST("/quote", orderHandler.Quote)
	engine.POST("/checkout", orderHandler.Checkout)
	quoteResponse := httptest.NewRecorder()
	engine.ServeHTTP(quoteResponse, httptest.NewRequestWithContext(ctx, http.MethodPost, "/quote", strings.NewReader(`{"plan_code":"distribution","billing_cycle":"monthly"}`)))
	if quoteResponse.Code != http.StatusOK {
		t.Fatalf("quote: %d %s", quoteResponse.Code, quoteResponse.Body.String())
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, "/checkout", strings.NewReader(fmt.Sprintf(`{"quote":%s}`, quoteResponse.Body.String()))))
	if response.Code != http.StatusCreated {
		t.Fatalf("checkout: %d %s quote=%s", response.Code, response.Body.String(), quoteResponse.Body.String())
	}
	var payload struct {
		Order struct {
			ID        int64 `json:"id"`
			ChannelID int64 `json:"channel_id"`
		} `json:"order"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Order.ChannelID != channel {
		t.Fatalf("attribution not captured: %s", response.Body.String())
	}
	if _, err := p.Exec(ctx, "INSERT INTO plans(code,name,price_cents,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('coupon-plan','{}',10000,1073741824,5,2,100)"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO coupons(code,kind,value,max_redemptions) VALUES('launch50','percent',50,1)"); err != nil {
		t.Fatal(err)
	}
	couponQuote := httptest.NewRecorder()
	engine.ServeHTTP(couponQuote, httptest.NewRequestWithContext(ctx, http.MethodPost, "/quote", strings.NewReader(`{"plan_code":"coupon-plan","billing_cycle":"monthly","coupon_code":"launch50"}`)))
	if couponQuote.Code != http.StatusOK {
		t.Fatalf("coupon quote: %d %s", couponQuote.Code, couponQuote.Body.String())
	}
	couponCheckout := httptest.NewRecorder()
	couponCheckoutRequest := httptest.NewRequestWithContext(ctx, http.MethodPost, "/checkout", strings.NewReader(fmt.Sprintf(`{"quote":%s}`, couponQuote.Body.String())))
	couponCheckoutRequest.Header.Set("Idempotency-Key", "coupon-checkout-1")
	engine.ServeHTTP(couponCheckout, couponCheckoutRequest)
	if couponCheckout.Code != http.StatusCreated {
		t.Fatalf("coupon checkout: %d %s", couponCheckout.Code, couponCheckout.Body.String())
	}
	var redemptionCount, redeemedCount int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM coupon_redemptions WHERE coupon_code='launch50' AND tenant_id=$1", tid).Scan(&redemptionCount); err != nil || redemptionCount != 1 {
		t.Fatalf("coupon redemption rows=%d err=%v", redemptionCount, err)
	}
	if err := p.QueryRow(ctx, "SELECT redeemed_count FROM coupons WHERE code='launch50'").Scan(&redeemedCount); err != nil || redeemedCount != 1 {
		t.Fatalf("coupon redeemed_count=%d err=%v", redeemedCount, err)
	}
	retry := httptest.NewRecorder()
	retryRequest := httptest.NewRequestWithContext(ctx, http.MethodPost, "/checkout", strings.NewReader(fmt.Sprintf(`{"quote":%s}`, couponQuote.Body.String())))
	retryRequest.Header.Set("Idempotency-Key", "coupon-checkout-1")
	engine.ServeHTTP(retry, retryRequest)
	if retry.Code != http.StatusCreated {
		t.Fatalf("idempotent coupon retry expected 201, got %d %s", retry.Code, retry.Body.String())
	}
	var retryPayload struct {
		Order struct {
			ID int64 `json:"id"`
		} `json:"order"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &retryPayload); err != nil || retryPayload.Order.ID == 0 {
		t.Fatalf("invalid retry response: %v %s", err, retry.Body.String())
	}
	var originalCouponOrder int64
	if err := p.QueryRow(ctx, "SELECT order_id FROM coupon_redemptions WHERE coupon_code='launch50' AND tenant_id=$1", tid).Scan(&originalCouponOrder); err != nil || retryPayload.Order.ID != originalCouponOrder {
		t.Fatalf("retry produced another order: retry=%d redeemed=%d err=%v", retryPayload.Order.ID, originalCouponOrder, err)
	}
	var newOrderID int64
	if err := p.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code) VALUES($1,'ORD-platform-coupon','manual','pending',100,'CNY','coupon-plan') RETURNING id", tid).Scan(&newOrderID); err != nil {
		t.Fatal(err)
	}
	platformHandler := NewPlatformHandler(store.New(p.Pool))
	platformEngine := gin.New()
	platformEngine.POST("/coupons/:code/redeem", platformHandler.RedeemCoupon)
	platformRedeem := func(tenantID, orderID int64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/coupons/launch50/redeem", strings.NewReader(fmt.Sprintf(`{"tenant_id":%d,"order_id":%d}`, tenantID, orderID)))
		req.Header.Set("Content-Type", "application/json")
		platformEngine.ServeHTTP(w, req)
		return w
	}
	var available int
	if err := p.QueryRow(ctx, "SELECT redeemed_count FROM coupons WHERE code='launch50'").Scan(&available); err != nil || available != 1 {
		t.Fatalf("coupon retry consumed capacity again: count=%d err=%v", available, err)
	}
	var exhaustedBrand, exhaustedTenant, exhaustedOrder int64
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('platform-coupon','Platform coupon') RETURNING id").Scan(&exhaustedBrand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Platform coupon','platform-coupon','active') RETURNING id", exhaustedBrand).Scan(&exhaustedTenant); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code) VALUES($1,'ORD-platform-exhausted','manual','pending',100,'CNY','coupon-plan') RETURNING id", exhaustedTenant).Scan(&exhaustedOrder); err != nil {
		t.Fatal(err)
	}
	if got := platformRedeem(exhaustedTenant, exhaustedOrder); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "COUPON_EXHAUSTED") {
		t.Fatalf("platform exhausted result: %d %s", got.Code, got.Body.String())
	}
	if err := p.QueryRow(ctx, "SELECT count(*) FROM coupon_redemptions WHERE coupon_code='launch50' AND tenant_id=$1", exhaustedTenant).Scan(&redemptionCount); err != nil || redemptionCount != 0 {
		t.Fatalf("platform exhaustion added reservation count=%d err=%v", redemptionCount, err)
	}
	var platformOrderID int64
	if err := p.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code) VALUES($1,'ORD-platform-new','manual','pending',100,'CNY','coupon-plan') RETURNING id", tid).Scan(&platformOrderID); err != nil {
		t.Fatal(err)
	}
	if got := platformRedeem(tid, platformOrderID); got.Code != http.StatusNotFound || !strings.Contains(got.Body.String(), "COUPON_NOT_FOUND") {
		t.Fatalf("second platform order reuse: %d %s", got.Code, got.Body.String())
	}
	var repeatTenant, repeatBrand, repeatOrder int64
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('coupon-repeat','Repeat') RETURNING id").Scan(&repeatBrand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Repeat','coupon-repeat','active') RETURNING id", repeatBrand).Scan(&repeatTenant); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code) VALUES($1,'ORD-platform-repeat','manual','pending',100,'CNY','coupon-plan') RETURNING id", repeatTenant).Scan(&repeatOrder); err != nil {
		t.Fatal(err)
	}
	if got := platformRedeem(repeatTenant, repeatOrder); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "COUPON_EXHAUSTED") {
		t.Fatalf("initial platform capacity exhaustion: %d %s", got.Code, got.Body.String())
	}
	if got := platformRedeem(repeatTenant, repeatOrder); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "COUPON_EXHAUSTED") {
		t.Fatalf("exhaustion retry should not silently succeed: %d %s", got.Code, got.Body.String())
	}
	secondQuote := httptest.NewRecorder()
	engine.ServeHTTP(secondQuote, httptest.NewRequestWithContext(ctx, http.MethodPost, "/quote", strings.NewReader(`{"plan_code":"coupon-plan","billing_cycle":"monthly","coupon_code":"launch50"}`)))
	if secondQuote.Code != http.StatusConflict {
		t.Fatalf("exhausted coupon should not quote, got %d %s", secondQuote.Code, secondQuote.Body.String())
	}
	var foreignTenant, foreignBrand int64
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('coupon-foreign','Foreign') RETURNING id").Scan(&foreignBrand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Foreign','coupon-foreign','active') RETURNING id", foreignBrand).Scan(&foreignTenant); err != nil {
		t.Fatal(err)
	}
	foreignTx, err := app.TenantTx(ctx, foreignTenant)
	if err != nil {
		t.Fatal(err)
	}
	// RLS hides the foreign order, so the invoker routine cannot see it and
	// rejects the reservation softly: no redemption row may appear. Either a
	// hard error or an 'invalid' result is an acceptable rejection; the
	// invariant under test is that nothing is consumed.
	var redemptionStatus string
	redemptionErr := foreignTx.QueryRow(ctx, "SELECT urmail_redeem_coupon('launch50',$1)", payload.Order.ID).Scan(&redemptionStatus)
	_ = foreignTx.Rollback(ctx)
	if redemptionErr == nil && redemptionStatus != "invalid" {
		t.Fatalf("cross-tenant order coupon redemption must be rejected, got %q", redemptionStatus)
	}
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := SettleOrder(ctx, p, payload.Order.ID); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	var count, amount int64
	if err := p.QueryRow(ctx, "SELECT count(*),COALESCE(sum(amount_cents),0) FROM commissions WHERE tenant_id=$1 AND order_id=$2", tid, payload.Order.ID).Scan(&count, &amount); err != nil || count != 1 || amount != 1000 {
		t.Fatalf("commissions count=%d amount=%d err=%v", count, amount, err)
	}
	if err := p.QueryRow(ctx, "SELECT count(*) FROM invoices WHERE tenant_id=$1", tid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("invoice count=%d err=%v", count, err)
	}
	if err := p.QueryRow(ctx, "SELECT count(*) FROM onboarding_tasks WHERE tenant_id=$1 AND order_id=$2 AND status='pending' AND step='domain_verification'", tid, payload.Order.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("settlement onboarding task count=%d err=%v", count, err)
	}
	if err := p.QueryRow(ctx, "SELECT count(*) FROM onboarding_tasks WHERE tenant_id=$1 AND order_id=$2", tid, payload.Order.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("order must have one onboarding task only, count=%d err=%v", count, err)
	}
	claimed, err := store.New(p.Pool).PlatformClaimOnboardingTask(ctx, store.PlatformClaimOnboardingTaskParams{WorkerID: "worker-a", LeaseSeconds: 60, BatchSize: 1})
	if err != nil || len(claimed) != 1 || claimed[0].LeasedBy.String != "worker-a" || claimed[0].Attempts != 1 {
		t.Fatalf("first claim=%+v err=%v", claimed, err)
	}
	stolen, err := store.New(p.Pool).PlatformClaimOnboardingTask(ctx, store.PlatformClaimOnboardingTaskParams{WorkerID: "worker-b", LeaseSeconds: 60, BatchSize: 1})
	if err != nil || len(stolen) != 0 {
		t.Fatalf("unexpired lease stolen: %+v err=%v", stolen, err)
	}
	rows, err := store.New(p.Pool).PlatformCompleteOnboardingTask(ctx, store.PlatformCompleteOnboardingTaskParams{TaskID: claimed[0].ID, WorkerID: "worker-b"})
	if err != nil || rows != 0 {
		t.Fatalf("wrong worker completed task: rows=%d err=%v", rows, err)
	}
	rows, err = store.New(p.Pool).PlatformFailOnboardingTask(ctx, store.PlatformFailOnboardingTaskParams{TaskID: claimed[0].ID, WorkerID: "worker-a", Error: "expected owner verification"})
	if err != nil || rows != 0 {
		t.Fatalf("unverified task accepted failure transition: rows=%d err=%v", rows, err)
	}
}
