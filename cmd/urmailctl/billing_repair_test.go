package main

import (
	"context"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestLegacyPeriodRepairRequiresExactEvidenceAndCommitsAuditAtomically(t *testing.T) {
	p, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	var brand, tenant, subscription, order int64
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('legacy-repair','Legacy') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Legacy','legacy-repair','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO plans(code,name,price_cents,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('legacy-repair','{}',10000,1073741824,5,2,100)"); err != nil {
		t.Fatal(err)
	}
	oldStart := time.Now().UTC().AddDate(100, 0, 0).Truncate(time.Microsecond)
	oldEnd := oldStart.AddDate(0, 1, 0)
	if err := p.QueryRow(ctx, "UPDATE subscriptions SET plan_id='legacy-repair',plan_snapshot=(SELECT to_jsonb(p) FROM plans p WHERE code='legacy-repair'),period_start=$2,period_end=$3 WHERE tenant_id=$1 AND status='active' RETURNING id", tenant, oldStart, oldEnd).Scan(&subscription); err != nil {
		t.Fatal(err)
	}
	paidAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := p.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code,billing_cycle,paid_at) VALUES($1,'legacy-paid','new','paid',8000,'CNY','legacy-repair','monthly',$2) RETURNING id", tenant, paidAt).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO payments(tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status,paid_at) VALUES($1,$2,'stripe','pi_legacy',8000,'CNY','succeeded',$3)", tenant, order, paidAt); err != nil {
		t.Fatal(err)
	}
	request := legacyPeriodRepair{TenantID: tenant, SubscriptionID: subscription, OrderID: order, NominalBasis: 10000, ExpectedStart: oldStart, ExpectedEnd: oldEnd, Reason: "Reviewed original nominal quote and the single confirmed receipt"}
	preview, err := repairLegacyPeriod(ctx, p, request, false)
	if err != nil || preview["changed"] != false {
		t.Fatalf("preview %v %v", preview, err)
	}
	var count int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM subscription_terms WHERE tenant_id=$1", tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview mutated %d %v", count, err)
	}
	bad := request
	bad.ExpectedEnd = oldEnd.Add(time.Hour)
	if _, err := repairLegacyPeriod(ctx, p, bad, true); err == nil {
		t.Fatal("stale expected snapshot was accepted")
	}
	if _, err := p.Exec(ctx, "CREATE FUNCTION reject_repair_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='REPAIR subscription.legacy_period' THEN RAISE EXCEPTION 'fixture audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_repair_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_repair_audit()"); err != nil {
		t.Fatal(err)
	}
	if _, err := repairLegacyPeriod(ctx, p, request, true); err == nil {
		t.Fatal("repair survived a failed mandatory audit")
	}
	var unchanged time.Time
	if err := p.QueryRow(ctx, "SELECT period_start FROM subscriptions WHERE id=$1", subscription).Scan(&unchanged); err != nil || !unchanged.Equal(oldStart) {
		t.Fatalf("audit failure changed period %s %v", unchanged, err)
	}
	if _, err := p.Exec(ctx, "DROP TRIGGER reject_repair_audit ON audit_logs; DROP FUNCTION reject_repair_audit()"); err != nil {
		t.Fatal(err)
	}
	if _, err := repairLegacyPeriod(ctx, p, request, true); err != nil {
		t.Fatal(err)
	}
	var corrected time.Time
	var basis int64
	if err := p.QueryRow(ctx, "SELECT period_start,base_amount_cents FROM subscription_terms WHERE tenant_id=$1", tenant).Scan(&corrected, &basis); err != nil || !corrected.Equal(paidAt) || basis != 10000 {
		t.Fatalf("corrected=%s basis=%d err=%v", corrected, basis, err)
	}
	if err := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='REPAIR subscription.legacy_period'", tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("repair audit %d %v", count, err)
	}
	if _, err := repairLegacyPeriod(ctx, p, request, true); err == nil {
		t.Fatal("repeated repair was accepted")
	}
}
