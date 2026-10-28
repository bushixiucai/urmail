package main

import "testing"

func ops009SeedRecurringSetups(t *testing.T, dsn string) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	_, e := conn.Exec(t.Context(), `DO $$ DECLARE tid bigint;sub bigint;actor bigint;merchant bigint;binding bigint;cfg timestamptz;period timestamptz;st text;n integer:=0; BEGIN
 SELECT id INTO tid FROM tenants WHERE slug='ops009-replay';
 SELECT id INTO actor FROM users WHERE email='ops009-owner@example.test';
 SELECT id,period_end INTO sub,period FROM subscriptions WHERE tenant_id=tid AND status='active' LIMIT 1;
 SELECT id,updated_at INTO merchant,cfg FROM payment_merchants WHERE account_id='acct_ops009recurring';
 SELECT id INTO binding FROM subscription_renewal_authorizations WHERE tenant_id=tid AND merchant_id=merchant LIMIT 1;
 FOREACH st IN ARRAY ARRAY['creating','ready','uncertain','verified','canceled'] LOOP
  n:=n+1;
  INSERT INTO recurring_setup_intents(tenant_id,subscription_id,actor_id,merchant_id,request_key,request_id,quote_digest,currency,expected_period_end,merchant_config_at,state,version,provider_reference,hosted_url,binding_id)
  VALUES(tid,sub,actor,merchant,'ops009-hosted-'||st,'00000000-0000-4000-8000-'||lpad(n::text,12,'0'),repeat('a',64),'CNY',period,cfg,st,4,
   CASE WHEN st='creating' THEN '' ELSE 'cs_ops009_'||st END,
   CASE WHEN st='ready' THEN 'https://checkout.stripe.com/c/pay/cs_ops009_ready' ELSE '' END,
   CASE WHEN st='verified' THEN binding ELSE NULL END);
 END LOOP;
 END $$`)
	if e != nil {
		t.Fatal(e)
	}
}
func ops009AssertRecurringSetups(t *testing.T, dsn string) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	checks := map[string]string{
		"unconsumed hosted proofs held":                `SELECT count(*)=3 AND bool_and(state='held' AND version=5 AND hosted_url='' AND expires_at<=now() AND last_error='RESTORE_RECONCILIATION_REQUIRED') FROM recurring_setup_intents WHERE request_key IN('ops009-hosted-creating','ops009-hosted-ready','ops009-hosted-uncertain')`,
		"verified provider and binding facts retained": `SELECT state='verified' AND version=4 AND provider_reference='cs_ops009_verified' AND binding_id IS NOT NULL AND quote_digest=repeat('a',64) FROM recurring_setup_intents WHERE request_key='ops009-hosted-verified'`,
		"canceled hosted facts retained":               `SELECT state='canceled' AND version=4 AND provider_reference='cs_ops009_canceled' AND hosted_url='' FROM recurring_setup_intents WHERE request_key='ops009-hosted-canceled'`,
		"unknown creation source retained":             `SELECT request_id='00000000-0000-4000-8000-000000000003' AND provider_reference='cs_ops009_uncertain' FROM recurring_setup_intents WHERE request_key='ops009-hosted-uncertain'`,
	}
	for name, sql := range checks {
		var pass bool
		if e := conn.QueryRow(t.Context(), sql).Scan(&pass); e != nil || !pass {
			t.Errorf("restored hosted setup %s: %v %v", name, pass, e)
		}
	}
}
