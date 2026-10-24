package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestLegacyPeriodRepairRequiresExactEvidenceAndCommitsAuditAtomically(t *testing.T) {
	for _, cycle := range []string{"monthly", "annual"} {
		t.Run(cycle, func(t *testing.T) { testLegacyPeriodRepairCalendar(t, cycle) })
	}
}

func testLegacyPeriodRepairCalendar(t *testing.T, cycle string) {
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
	// Both instants cross the UTC day boundary; UTC inference gives the wrong
	// nominal day and loses the Shanghai leap-month or leap-year anniversary.
	paidAt := time.Date(2024, 1, 30, 16, 30, 0, 0, time.UTC)
	expectedEnd := time.Date(2024, 2, 28, 16, 30, 0, 0, time.UTC)
	if cycle == "annual" {
		paidAt = time.Date(2024, 2, 28, 16, 30, 0, 0, time.UTC)
		expectedEnd = time.Date(2025, 2, 27, 16, 30, 0, 0, time.UTC)
	}
	if err := p.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code,billing_cycle,paid_at) VALUES($1,'legacy-paid','new','paid',8000,'CNY','legacy-repair',$3,$2) RETURNING id", tenant, paidAt, cycle).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO payments(tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status,paid_at) VALUES($1,$2,'stripe','pi_legacy',8000,'CNY','succeeded',$3)", tenant, order, paidAt); err != nil {
		t.Fatal(err)
	}
	request := legacyPeriodRepair{TenantID: tenant, SubscriptionID: subscription, OrderID: order, NominalBasis: 10000, ExpectedStart: oldStart, ExpectedEnd: oldEnd, Reason: "Reviewed original nominal quote and the single confirmed receipt"}
	if _, err := repairLegacyPeriod(ctx, p, request, false); err == nil {
		t.Fatal("repair inferred a historical timezone without a reviewed calendar")
	}
	var actor int64
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('reviewer@example.test','unused') RETURNING id").Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO platform_admins(user_id) VALUES($1)", actor); err != nil {
		t.Fatal(err)
	}
	// A determination bound to a different receipt must never grant rights.
	if _, err := p.Exec(ctx, "INSERT INTO billing_calendars(tenant_id,timezone,first_paid_at,anchor_at,source,review_reason,created_by) VALUES($1,'Asia/Shanghai',$2,$2,'manual_legacy','Reviewed unique historical receipt and owner timezone',$3)", tenant, paidAt.Add(time.Hour), actor); err != nil {
		t.Fatal(err)
	}
	if _, err := repairLegacyPeriod(ctx, p, request, true); err == nil {
		t.Fatal("contradictory calendar receipt accepted")
	}
	if _, err := p.Exec(ctx, "DELETE FROM billing_calendars WHERE tenant_id=$1", tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO billing_calendars(tenant_id,timezone,first_paid_at,anchor_at,source,review_reason,created_by) VALUES($1,'Asia/Shanghai',$2,$2,'manual_legacy','Reviewed unique historical receipt and owner timezone',$3)", tenant, paidAt, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO invoices(tenant_id,number,period_start,period_end,amount_cents,currency,status) VALUES($1,'original-paid-invoice',$2,$3,8000,'CNY','paid')", tenant, oldStart, oldEnd); err != nil {
		t.Fatal(err)
	}
	var originalInvoice []byte
	if err := p.QueryRow(ctx, "SELECT to_jsonb(i) FROM invoices i WHERE tenant_id=$1", tenant).Scan(&originalInvoice); err != nil {
		t.Fatal(err)
	}
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
	if err := p.QueryRow(ctx, "SELECT count(*) FROM subscription_terms WHERE tenant_id=$1", tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("audit failure left a period term: count=%d err=%v", count, err)
	}
	if _, err := p.Exec(ctx, "DROP TRIGGER reject_repair_audit ON audit_logs; DROP FUNCTION reject_repair_audit()"); err != nil {
		t.Fatal(err)
	}
	if _, err := repairLegacyPeriod(ctx, p, request, true); err != nil {
		t.Fatal(err)
	}
	var corrected, correctedEnd time.Time
	var calendarSnapshot []byte
	var basis int64
	if err := p.QueryRow(ctx, "SELECT period_start,period_end,base_amount_cents,calendar_snapshot FROM subscription_terms WHERE tenant_id=$1", tenant).Scan(&corrected, &correctedEnd, &basis, &calendarSnapshot); err != nil || !corrected.Equal(paidAt) || !correctedEnd.Equal(expectedEnd) || basis != 10000 {
		t.Fatalf("corrected=%s basis=%d err=%v", corrected, basis, err)
	}
	var snapshot struct {
		Timezone string `json:"timezone"`
		Wall     struct {
			Day int `json:"day"`
		} `json:"wall"`
	}
	if err := json.Unmarshal(calendarSnapshot, &snapshot); err != nil || snapshot.Timezone != "Asia/Shanghai" || snapshot.Wall.Day != paidAt.In(time.FixedZone("Shanghai", 8*3600)).Day() {
		t.Fatalf("persisted calendar snapshot invalid: %v", err)
	}
	var finalInvoice []byte
	if err := p.QueryRow(ctx, "SELECT to_jsonb(i) FROM invoices i WHERE tenant_id=$1", tenant).Scan(&finalInvoice); err != nil || string(finalInvoice) != string(originalInvoice) {
		t.Fatal("repair rewrote historical paid invoice")
	}
	if err := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='REPAIR subscription.legacy_period'", tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("repair audit %d %v", count, err)
	}
	if _, err := repairLegacyPeriod(ctx, p, request, true); err == nil {
		t.Fatal("repeated repair was accepted")
	}
}
