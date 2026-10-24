package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestCouponReissueIsTenantBoundIdempotentAndAuditAtomic(t *testing.T) {
	p, a := authIntegrationDatabase(t)
	ctx := context.Background()
	q := store.New(p.Pool)
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := p.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := p.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('reissue','Reissue') RETURNING id")
	actor := scalar("INSERT INTO users(email,password_hash) VALUES('reissue@fixture.test','unused') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'A','reissue-a','active') RETURNING id", brand)
	other := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'B','reissue-b','active') RETURNING id", brand)
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", actor)
	exec("INSERT INTO user_tenant_roles VALUES($1,$2,'owner'),($1,$3,'owner')", actor, tenant, other)
	exec("INSERT INTO coupons(code,kind,value,max_redemptions) VALUES('lost-trial','trial_days',7,10),('rollback-trial','trial_days',7,10)")
	// Seed a pre-0050 wasted redemption without weakening the installed production guard.
	exec("ALTER TABLE coupon_redemptions DISABLE TRIGGER trial_redemption_guard")
	exec("INSERT INTO coupon_redemptions(coupon_code,tenant_id) VALUES('lost-trial',$1),('rollback-trial',$1)", tenant)
	exec("ALTER TABLE coupon_redemptions ENABLE TRIGGER trial_redemption_guard")
	h := &CouponReissueHandler{Store: q}
	r := gin.New()
	r.Use(middleware.AuditPlatform(q, zap.NewNop()), func(c *gin.Context) { c.Set(middleware.ContextUserID, actor) })
	r.POST("/api/v1/platform/tenants/:id/coupon-reissues", middleware.RequirePlatformTx(p, zap.NewNop()), h.Issue)
	call := func(tid int64, source string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"source_code": source, "reason": "Explicit replacement for verified wasted legacy redemption", "valid_until": time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339)})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("/api/v1/platform/tenants/%d/coupon-reissues", tid), strings.NewReader(string(body))))
		return w
	}
	if w := call(other, "lost-trial"); w.Code != 409 {
		t.Fatalf("cross-tenant source accepted %d %s", w.Code, w.Body)
	}
	w := call(tenant, "lost-trial")
	if w.Code != 201 {
		t.Fatalf("issue %d %s", w.Code, w.Body)
	}
	var result struct {
		Coupon store.Coupons `json:"coupon"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w = call(tenant, "lost-trial"); w.Code != 200 {
		t.Fatalf("idempotent %d %s", w.Code, w.Body)
	}
	if count := scalar("SELECT count(*) FROM coupons WHERE source_coupon_code='lost-trial'"); count != 1 {
		t.Fatalf("duplicate replacement %d", count)
	}
	for _, tid := range []int64{tenant, other} {
		tx, err := a.UserTx(ctx, tid, actor)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.New(tx).GetCouponByCode(ctx, result.Coupon.Code)
		_ = tx.Rollback(ctx)
		if (err == nil) != (tid == tenant) {
			t.Fatalf("coupon visibility mismatch tenant%d err%v", tid, err)
		}
	}
	// The SECURITY DEFINER trial path must still enforce domain verification, paid protection and once-only delivery.
	tx, err := a.UserTx(ctx, other, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SELECT urmail_redeem_coupon($1,NULL)", result.Coupon.Code); err == nil {
		t.Fatal("foreign tenant redeemed replacement")
	}
	_ = tx.Rollback(ctx)
	if count := scalar("SELECT count(*) FROM tenant_trials"); count != 0 {
		t.Fatal("issuance granted trial rights")
	}
	exec("INSERT INTO coupons(code,kind,value,max_redemptions) VALUES('refunded-discount','percent',10,10)")
	ordinaryOrder := scalar("INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code) VALUES($1,'REFUND-SOURCE','new','paid',100,'CNY','free') RETURNING id", tenant)
	paymentID := scalar("INSERT INTO payments(tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status) VALUES($1,$2,'manual','refund-source-fixture',100,'CNY','succeeded') RETURNING id", tenant, ordinaryOrder)
	exec("INSERT INTO coupon_redemptions(coupon_code,tenant_id,order_id) VALUES('refunded-discount',$1,$2)", tenant, ordinaryOrder)
	if w = call(tenant, "refunded-discount"); w.Code != 409 {
		t.Fatalf("ordinary coupon returned without confirmed refund: %d %s", w.Code, w.Body)
	}
	scalar("INSERT INTO refunds(tenant_id,order_id,payment_id,refund_no,amount_cents,currency,status,reason,approved_by,approval_reason) VALUES($1,$2,$3,'REFUND-SOURCE',50,'CNY','completed','Confirmed partial refund',$4,'Explicit platform approval fixture') RETURNING id", tenant, ordinaryOrder, paymentID, actor)
	if w = call(tenant, "refunded-discount"); w.Code != 201 {
		t.Fatalf("explicit refunded coupon replacement failed: %d %s", w.Code, w.Body)
	}
	exec(`CREATE FUNCTION reject_reissue_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.detail->>'outcome'='succeeded' THEN RAISE EXCEPTION 'audit fixture unavailable'; END IF; RETURN NEW; END $$;CREATE TRIGGER reject_reissue_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_reissue_audit()`)
	if w = call(tenant, "rollback-trial"); w.Code != 500 {
		t.Fatalf("audit failure %d %s", w.Code, w.Body)
	}
	if count := scalar("SELECT count(*) FROM coupons WHERE source_coupon_code='rollback-trial'"); count != 0 {
		t.Fatal("replacement survived failed audit")
	}
	exec("DROP TRIGGER reject_reissue_audit ON audit_logs")
	exec(`INSERT INTO plans(code,name,description,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features) VALUES('reissue-trial','{}','{}',100,'CNY',1073741824,5,2,100,'{"trial_eligible":true}')`)
	otherDomain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'foreign-reissue.test','active',now(),'proof') RETURNING id", other)
	tx, err = a.UserTx(ctx, other, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.New(tx).RedeemTenantTrial(ctx, store.RedeemTenantTrialParams{TenantID: other, DomainID: otherDomain, CouponCode: result.Coupon.Code, PlanCode: "reissue-trial"}); err == nil {
		t.Fatal("foreign tenant used bound trial through definer path")
	}
	_ = tx.Rollback(ctx)
	ownDomain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verification_token) VALUES($1,'own-reissue.test','pending_dns','proof') RETURNING id", tenant)
	tx, err = a.UserTx(ctx, tenant, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.New(tx).RedeemTenantTrial(ctx, store.RedeemTenantTrialParams{TenantID: tenant, DomainID: ownDomain, CouponCode: result.Coupon.Code, PlanCode: "reissue-trial"}); err == nil {
		t.Fatal("replacement bypassed domain verification")
	}
	_ = tx.Rollback(ctx)
	exec("UPDATE domains SET status='active',verified_at=now() WHERE tenant_id=$1 AND id=$2", tenant, ownDomain)
	tx, err = a.UserTx(ctx, tenant, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.New(tx).RedeemTenantTrial(ctx, store.RedeemTenantTrialParams{TenantID: tenant, DomainID: ownDomain, CouponCode: result.Coupon.Code, PlanCode: "reissue-trial"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = a.UserTx(ctx, tenant, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.New(tx).RedeemTenantTrial(ctx, store.RedeemTenantTrialParams{TenantID: tenant, DomainID: ownDomain, CouponCode: result.Coupon.Code, PlanCode: "reissue-trial"}); err == nil {
		t.Fatal("replacement bypassed once-only trial")
	}
	_ = tx.Rollback(ctx)
	if count := scalar("SELECT count(*) FROM tenant_trials WHERE tenant_id=$1", tenant); count != 1 {
		t.Fatal("trial was not delivered exactly once")
	}

}
