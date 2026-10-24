package handlers

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestTenantTrialConcurrentRedemptionAndPaidProtection(t *testing.T) {
	p, a := authIntegrationDatabase(t)
	ctx := context.Background()
	var brand, tid, uid, did int64
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('trial','Trial') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('trial@example.test','unused') RETURNING id").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Trial','trial','active') RETURNING id", brand).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		fmt.Sprintf("INSERT INTO user_tenant_roles VALUES(%d,%d,'owner')", uid, tid),
		`INSERT INTO plans(code,name,description,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features) VALUES('trial-plan','{}','{}',10000,'CNY',1073741824,5,2,100,'{"trial_eligible":true}')`,
		`INSERT INTO coupons(code,kind,value,max_redemptions) VALUES('trial-once','trial_days',7,10)`,
	} {
		if _, err := p.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'trial.example.test','active',now(),'token') RETURNING id", tid).Scan(&did); err != nil {
		t.Fatal(err)
	}
	var success atomic.Int32
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := a.UserTx(ctx, tid, uid)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			_, err = store.New(tx).RedeemTenantTrial(ctx, store.RedeemTenantTrialParams{TenantID: tid, DomainID: did, CouponCode: "trial-once", PlanCode: "trial-plan"})
			if err == nil {
				if err = tx.Commit(ctx); err != nil {
					t.Error(err)
				} else {
					success.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("expected one trial, got %d", success.Load())
	}
	var trials, redemptions, payments int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM tenant_trials WHERE tenant_id=$1", tid).Scan(&trials); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "SELECT redeemed_count FROM coupons WHERE code='trial-once'").Scan(&redemptions); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "SELECT count(*) FROM payments WHERE tenant_id=$1", tid).Scan(&payments); err != nil {
		t.Fatal(err)
	}
	if trials != 1 || redemptions != 1 || payments != 0 {
		t.Fatalf("inconsistent trial facts: %d %d %d", trials, redemptions, payments)
	}
	var paidTid, paidDid int64
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Paid','paid-trial','active') RETURNING id", brand).Scan(&paidTid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO user_tenant_roles VALUES($1,$2,'owner')", uid, paidTid); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'paid-trial.example.test','active',now(),'token') RETURNING id", paidTid).Scan(&paidDid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "UPDATE subscriptions SET plan_id='trial-plan' WHERE tenant_id=$1 AND status='active'", paidTid); err != nil {
		t.Fatal(err)
	}
	tx, err := a.UserTx(ctx, paidTid, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = store.New(tx).RedeemTenantTrial(ctx, store.RedeemTenantTrialParams{TenantID: paidTid, DomainID: paidDid, CouponCode: "trial-once", PlanCode: "trial-plan"}); err == nil {
		t.Fatal("paid entitlement was overwritten by trial")
	}
}
