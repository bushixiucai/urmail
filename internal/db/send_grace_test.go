package db_test

import (
	"context"
	"testing"

	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestDelinquencyKeepsInboundAfterGrace(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, plan int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('grace','Grace') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'G','grace-g','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM plans WHERE code='free')").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Skip("free plan is not seeded in this fixture")
	}
	_ = plan
	if _, err = tx.Exec(ctx, "UPDATE subscriptions SET status='past_due', period_start=now()-interval '40 days', period_end=now()-interval '1 day' WHERE tenant_id=$1", tenant); err != nil {
		t.Fatal(err)
	}
	var send, read bool
	if err = tx.QueryRow(ctx, "SELECT urmail_mail_allowed($1), urmail_mail_readable($1)", tenant).Scan(&send, &read); err != nil {
		t.Fatal(err)
	}
	if !send || !read {
		t.Fatalf("day 1 grace send=%v read=%v", send, read)
	}
	if _, err = tx.Exec(ctx, "UPDATE subscriptions SET period_end=now()-interval '8 days' WHERE tenant_id=$1", tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "SELECT urmail_mail_allowed($1), urmail_mail_readable($1)", tenant).Scan(&send, &read); err != nil {
		t.Fatal(err)
	}
	if send || !read {
		t.Fatalf("day 8 send=%v read=%v", send, read)
	}
	if _, err = tx.Exec(ctx, "UPDATE subscriptions SET period_end=(now() AT TIME ZONE 'UTC')::date - interval '7 days' + interval '12 hours' WHERE tenant_id=$1", tenant); err != nil {
		t.Fatal(err)
	}
	tag, err := tx.Exec(ctx, "WITH configured_days AS (SELECT 0::int AS days), reminders AS (SELECT s.tenant_id,t.owner_user_id,NULL::text AS email,s.period_end,s.plan_id,d.days FROM subscriptions s JOIN tenants t ON t.id=s.tenant_id CROSS JOIN configured_days d WHERE s.tenant_id=$1 AND ((s.period_end AT TIME ZONE 'UTC')::date=(now() AT TIME ZONE 'UTC')::date+d.days OR (s.period_end AT TIME ZONE 'UTC')::date+7=(now() AT TIME ZONE 'UTC')::date)) INSERT INTO notification_outbox(tenant_id,kind,message_key,params,dedupe_key,channel,state) SELECT tenant_id,'subscription_expiry','notification.subscription_expiry',jsonb_build_object('days',days), 'grace-test:'||tenant_id||':'||extract(epoch FROM period_end)::text, 'in_app','delivered' FROM reminders ON CONFLICT(dedupe_key) DO NOTHING", tenant)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("grace reminder rows=%v err=%v", tag, err)
	}
}

func TestRenewalFailureDoesNotExtendService(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, sub int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('renew-fail','Renew') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'R','renew-r','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO plans(code,name,price_cents,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features) SELECT 'paid-renew','{\"en\":\"Paid\"}',100,1,1,1,1,'{}'::jsonb WHERE NOT EXISTS(SELECT 1 FROM plans WHERE code='paid-renew')"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE subscriptions SET plan_id='paid-renew', status='active', cancel_at_period_end=false, period_start=now()-interval '27 days', period_end=now()+interval '1 day' WHERE tenant_id=$1", tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "SELECT id FROM subscriptions WHERE tenant_id=$1", tenant).Scan(&sub); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO subscription_renewal_attempts(tenant_id,subscription_id,period_end,state) SELECT tenant_id,id,period_end,'pending' FROM subscriptions WHERE id=$1", sub); err != nil {
		t.Fatal(err)
	}
	var before string
	if err = tx.QueryRow(ctx, "SELECT period_end::text FROM subscriptions WHERE id=$1", sub).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE subscription_renewal_attempts SET state='failed', attempts=5, last_error='NO_SAVED_PAYMENT_METHOD' WHERE subscription_id=$1", sub); err != nil {
		t.Fatal(err)
	}
	var after, status string
	if err = tx.QueryRow(ctx, "SELECT period_end::text,status FROM subscriptions WHERE id=$1", sub).Scan(&after, &status); err != nil {
		t.Fatal(err)
	}
	if after != before || status != "active" {
		t.Fatalf("renewal failure changed service before=%s after=%s status=%s", before, after, status)
	}
}
