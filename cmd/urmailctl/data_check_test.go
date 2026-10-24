package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestDataCheckInventoriesUnreviewedCalendarsWithoutChangingRights(t *testing.T) {
	p, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
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
	brand := scalar("INSERT INTO brands(slug,name) VALUES('calendar-inventory','Inventory') RETURNING id")
	actor := scalar("INSERT INTO users(email,password_hash) VALUES('calendar-inventory@example.test','unused') RETURNING id")
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", actor)
	exec("INSERT INTO plans(code,name,price_cents,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('calendar-inventory','{}',100,1024,5,2,100)")
	tenant := func(name string) int64 {
		return scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,$2,$2,'active') RETURNING id", brand, name)
	}
	unknown, zeroPaid, trial := tenant("unknown-calendar"), tenant("zero-paid-calendar"), tenant("proven-trial-calendar")
	first := time.Date(2024, 1, 30, 16, 30, 0, 0, time.UTC)
	exec("UPDATE subscriptions SET plan_id='calendar-inventory',plan_snapshot=(SELECT to_jsonb(p) FROM plans p WHERE code='calendar-inventory') WHERE tenant_id=$1", unknown)
	exec("INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code,billing_cycle,paid_at) VALUES($1,'zero-paid-calendar','new','paid',0,'CNY','calendar-inventory','monthly',$2)", zeroPaid, first)
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verification_token) VALUES($1,'trial-inventory.test','pending_dns','private-fixture') RETURNING id", trial)
	exec("INSERT INTO coupons(code,kind,value) VALUES('calendar-inventory-trial','trial_days',7)")
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	end := start.Add(7 * 24 * time.Hour)
	exec("INSERT INTO tenant_trials(tenant_id,domain_id,coupon_code,plan_code,plan_snapshot,started_at,ends_at,redeemed_by) VALUES($1,$2,'calendar-inventory-trial','calendar-inventory','{}',$3,$4,$5)", trial, domain, start, end, actor)
	exec("UPDATE subscriptions SET plan_id='calendar-inventory',plan_snapshot=(SELECT to_jsonb(p) FROM plans p WHERE code='calendar-inventory'),period_start=$2,period_end=$3,trial_ends_at=$3 WHERE tenant_id=$1", trial, start, end)
	snapshot := func() string {
		t.Helper()
		var raw []byte
		if err := p.QueryRow(ctx, "SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM subscriptions s").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	before := snapshot()
	read := func() (int64, []int64) {
		t.Helper()
		var output bytes.Buffer
		err := writeDataCheck(ctx, p, &output)
		var report struct {
			Count     int64   `json:"billing_calendar_review_tenant_count"`
			IDs       []int64 `json:"billing_calendar_review_tenant_ids"`
			Truncated bool    `json:"billing_calendar_review_ids_truncated"`
			Changed   bool    `json:"changed"`
		}
		if decodeErr := json.Unmarshal(output.Bytes(), &report); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if report.Changed || report.Truncated {
			t.Fatal("read-only inventory claimed changes or truncation")
		}
		if report.Count > 0 && (err == nil || !strings.Contains(err.Error(), "billing calendar review")) {
			t.Fatal("operator summary omitted pending calendar review")
		}
		return report.Count, report.IDs
	}
	count, ids := read()
	if count != 2 || len(ids) != 2 || ids[0] != unknown || ids[1] != zeroPaid {
		t.Fatalf("count=%d ids=%v; unknown and zero-paid must be inventoried, proven trial excluded", count, ids)
	}
	if snapshot() != before {
		t.Fatal("read-only inventory changed subscription rights")
	}
	for _, id := range []int64{unknown, zeroPaid} {
		exec("INSERT INTO billing_calendars(tenant_id,timezone,first_paid_at,anchor_at,source,review_reason,created_by) VALUES($1,'Asia/Shanghai',$2,$2,'manual_legacy','Operator verified historical timezone and initial paid facts',$3)", id, first, actor)
	}
	count, ids = read()
	if count != 0 || len(ids) != 0 {
		t.Fatalf("determined calendars remained in inventory: %d %v", count, ids)
	}
	if snapshot() != before {
		t.Fatal("calendar determination or inventory rewrote existing entitlements")
	}
}
