package main

import (
	"context"
	"testing"

	"github.com/bushixiucai/urmail/internal/billingrenew"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

func ops009SeedCalendarRecurring(t *testing.T, dsn string) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	_, err := conn.Exec(t.Context(), `DO $$ DECLARE t bigint;m bigint;c bigint;e bigint;s bigint;u bigint;merchant bigint;binding bigint;enrollment bigint;cfg timestamptz;period timestamptz; BEGIN
 SELECT id INTO t FROM tenants WHERE slug='ops009-replay';
 SELECT id INTO m FROM mailboxes WHERE tenant_id=t LIMIT 1;
 SELECT id INTO c FROM mailbox_calendars WHERE tenant_id=t AND mailbox_id=m LIMIT 1;
 SELECT id INTO u FROM users WHERE email='ops009-owner@example.test';
 INSERT INTO mailbox_events(tenant_id,mailbox_id,calendar_id,uid,summary,starts_at,ends_at,timezone,organizer,invitation_sequence,invitation_stamp,invitation_digest,invitation_response,reminders)
 VALUES(t,m,c,'ops009-scheduling','Restored meeting',now()+interval '1 minute',now()+interval '1 hour','UTC','host@external.example',4,now(),repeat('c',64),'accepted','[5]') RETURNING id INTO e;
 INSERT INTO mailbox_jobs(tenant_id,mailbox_id,kind,idempotency_key,payload,status,lease_until) VALUES
 (t,m,'calendar_reminder','ops009-reminder-pending',jsonb_build_object('event_id',e,'version',1,'fire_at',now()-interval '4 minutes','summary','Restored meeting'),'pending',now()-interval '4 minutes'),
 (t,m,'calendar_reminder','ops009-reminder-done',jsonb_build_object('event_id',e,'version',1,'fire_at',now()-interval '4 minutes','summary','Restored meeting'),'done',NULL),
 (t,m,'calendar_response','ops009-response-unknown',jsonb_build_object('event_id',e,'uid','ops009-scheduling','sequence',4,'action','accept'),'uncertain',NULL),
 (t,m,'calendar_response','ops009-response-done',jsonb_build_object('event_id',e,'uid','ops009-scheduling','sequence',3,'action','accept'),'done',NULL);
 SELECT id,period_end INTO s,period FROM subscriptions WHERE tenant_id=t AND status='active' LIMIT 1;
 INSERT INTO payment_merchants(provider,environment,account_id,name,enabled,currencies,entrypoint,encrypted_config) VALUES('stripe','sandbox','acct_ops009recurring','OPS009 isolated approval fact',true,ARRAY['CNY'],'hosted','enc:v1:fixture') RETURNING id,updated_at INTO merchant,cfg;
 INSERT INTO payment_recurring_capabilities(merchant_id,adapter_key,merchant_approved,runtime_available,evidence_at,evidence_expires_at,evidence_kind,merchant_config_at,evidence_reference) VALUES(merchant,'ops009-isolated-adapter',true,true,now(),now()+interval '1 day','sandbox',cfg,'isolated pre-backup approval fact');
 INSERT INTO subscription_renewal_authorizations(tenant_id,subscription_id,merchant_id,authorization_ciphertext,authorization_hash,label,currency,verified_at,expires_at,merchant_config_at) VALUES(t,s,merchant,'enc:v1:fixture',repeat('d',64),'isolated pre-backup authorization','CNY',now(),now()+interval '1 year',cfg) RETURNING id INTO binding;
 INSERT INTO subscription_renewal_enrollments(tenant_id,subscription_id,binding_id,state,consent_version,consented_by,consent_quote_digest,amount_cents,currency,billing_cycle,plan_id,plan_snapshot,plan_snapshot_digest,expected_period_end,renewal_period_end) VALUES(t,s,binding,'active','urmail-auto-renew-v1',u,repeat('e',64),1000,'CNY','monthly','free','{"code":"free"}',repeat('f',64),period,period+interval '1 month') RETURNING id INTO enrollment;
 INSERT INTO subscription_renewal_attempts(tenant_id,subscription_id,period_end,state,enrollment_id,binding_id,merchant_id,merchant_scope,amount_cents,currency,billing_cycle,plan_snapshot,plan_snapshot_digest,claim_token,lease_until)
 VALUES(t,s,period,'preparing',enrollment,binding,merchant,'stripe:sandbox:acct_ops009recurring',1000,'CNY','monthly','{"code":"free"}',repeat('f',64),4,now()-interval '1 hour');
 INSERT INTO subscription_renewal_attempts(tenant_id,subscription_id,period_end,state,enrollment_id,binding_id,merchant_id,merchant_scope,amount_cents,currency,billing_cycle,plan_snapshot,plan_snapshot_digest,claim_token,lease_until,side_effect_intent_at,dedupe_key,charge_count)
 VALUES(t,s,period+interval '1 second','charging',enrollment,binding,merchant,'stripe:sandbox:acct_ops009recurring',1000,'CNY','monthly','{"code":"free"}',repeat('f',64),7,now()-interval '1 hour',now()-interval '2 hours','ops009-existing-financial-intent',1);
 END $$`)
	if err != nil {
		t.Fatal(err)
	}
}

type ops009RejectRenewalResolver struct{ calls int }

func (r *ops009RejectRenewalResolver) ChargerFor(context.Context, store.SubscriptionRenewalAttempts) (billingrenew.Charger, billingrenew.ChargeRequest, error) {
	r.calls++
	return nil, billingrenew.ChargeRequest{}, billingrenew.ErrNoChargingChannel
}

func ops009AssertCalendarRecurring(t *testing.T, dsn string) {
	t.Helper()
	ctx := t.Context()
	conn := ops009Connect(t, dsn)
	checks := map[string]string{
		"calendar identity retained":                  `SELECT organizer='host@external.example' AND invitation_sequence=4 AND invitation_response='accepted' AND reminders='[5]'::jsonb FROM mailbox_events WHERE uid='ops009-scheduling'`,
		"pending reminder held":                       `SELECT status='uncertain' AND last_error LIKE '%RESTORE_RECONCILIATION_REQUIRED%' FROM mailbox_jobs WHERE idempotency_key='ops009-reminder-pending'`,
		"unknown response held":                       `SELECT status='uncertain' AND last_error LIKE '%RESTORE_RECONCILIATION_REQUIRED%' FROM mailbox_jobs WHERE idempotency_key='ops009-response-unknown'`,
		"completed response retained":                 `SELECT status='done' FROM mailbox_jobs WHERE idempotency_key='ops009-response-done'`,
		"unstarted renewal canceled":                  `SELECT state='canceled' AND side_effect_intent_at IS NULL AND claim_token>4 AND lease_until IS NULL FROM subscription_renewal_attempts WHERE claim_token>4 AND enrollment_id IS NOT NULL AND dedupe_key=''`,
		"charged intent never discarded":              `SELECT state='uncertain' AND side_effect_intent_at IS NOT NULL AND dedupe_key='ops009-existing-financial-intent' AND claim_token>7 AND lease_until IS NULL AND amount_cents=1000 FROM subscription_renewal_attempts WHERE dedupe_key='ops009-existing-financial-intent'`,
		"consent requires fresh authorization":        `SELECT state='needs_reauthorization' AND consented_at IS NOT NULL AND consent_version='urmail-auto-renew-v1' FROM subscription_renewal_enrollments`,
		"provider authorization revoked after rewind": `SELECT revoked_at IS NOT NULL AND authorization_ciphertext='enc:v1:fixture' FROM subscription_renewal_authorizations`,
		"merchant evidence not replayed":              `SELECT NOT merchant_approved AND NOT runtime_available AND evidence_at IS NOT NULL FROM payment_recurring_capabilities`,
	}
	for name, query := range checks {
		var ok bool
		if err := conn.QueryRow(ctx, query).Scan(&ok); err != nil || !ok {
			t.Errorf("restored scheduling/recurring %s: %v %v", name, ok, err)
		}
	}
	platform, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer platform.Close()
	// After the real restore and explicit quarantine release, ordinary workers
	// still cannot re-charge/reply or reconstruct a past alarm from the snapshot.
	resolver := &ops009RejectRenewalResolver{}
	worker := billingrenew.Worker{Store: store.New(platform), Chargers: resolver, Coordinator: billingrenew.PoolCoordinator{Pool: platform}, Settlements: billingrenew.PoolSettlementStore{Pool: platform}}
	for range 2 {
		if err = worker.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if err = (appcollab.ReminderWorker{Pool: platform}).RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if resolver.calls != 0 {
		t.Fatal("restored financial intent escaped review into provider dispatch")
	}
	var done int
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM mailbox_jobs WHERE kind='calendar_reminder' AND status='done'").Scan(&done); err != nil || done != 1 {
		t.Fatalf("past reminders were reconstructed %d %v", done, err)
	}
}
