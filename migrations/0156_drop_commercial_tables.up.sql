-- Drop commercial and self-registration tables. Historical migrations stay
-- untouched. Functions and triggers on kept tables that still read those
-- tables are detached first, so tenant, domain and mailbox writes survive.

CREATE OR REPLACE FUNCTION urmail_mail_allowed(p_tenant bigint) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT EXISTS(
  SELECT 1 FROM tenants t
  WHERE t.id=p_tenant AND t.status='active'
   AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=t.id)
    OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=t.id AND gd.status='active' AND gd.provisioning_state='ready'))
 );
$$;

CREATE OR REPLACE FUNCTION urmail_mail_readable(p_tenant bigint) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT EXISTS(SELECT 1 FROM tenants t WHERE t.id=p_tenant AND t.status='active');
$$;

CREATE OR REPLACE FUNCTION urmail_resource_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tid bigint; configured bigint;
BEGIN
 tid:=COALESCE(NEW.tenant_id,OLD.tenant_id);
 PERFORM pg_advisory_xact_lock(tid);
 IF TG_OP='DELETE' THEN
  IF TG_TABLE_NAME='mailboxes' AND (OLD.status<>'deleted' OR OLD.usage_measured_at IS NULL OR OLD.usage_error IS NOT NULL OR OLD.usage_measured_at<now()-interval '10 minutes' OR OLD.bytes_used>0) THEN RAISE EXCEPTION 'STORAGE_NOT_EMPTY' USING ERRCODE='23514'; END IF;
  RETURN OLD;
 END IF;
 IF TG_OP='UPDATE' THEN
  IF TG_TABLE_NAME='domains' AND NEW.tenant_id=OLD.tenant_id THEN RETURN NEW; END IF;
  IF TG_TABLE_NAME='mailboxes' AND NEW.tenant_id=OLD.tenant_id AND NEW.quota_bytes=OLD.quota_bytes AND NOT(OLD.status='deleted' AND NEW.status<>'deleted') THEN RETURN NEW; END IF;
 END IF;
 IF NOT EXISTS(SELECT 1 FROM tenants WHERE id=tid AND status IN('active','pending')) THEN RAISE EXCEPTION 'SUBSCRIPTION_EXPIRED' USING ERRCODE='23514'; END IF;
 IF TG_TABLE_NAME='domains' THEN RETURN NEW; END IF;
 IF NOT urmail_domain_mail_ready(tid,NEW.domain_id) AND NOT (
  (current_setting('role',true) IN('urmail_platform','urmail_migrate')
   OR (current_setting('role',true)='none' AND pg_has_role(session_user,'urmail_platform','USAGE')))
 ) THEN RAISE EXCEPTION 'DOMAIN_NOT_PROVISIONED' USING ERRCODE='23514'; END IF;
 IF TG_OP='INSERT' THEN NEW.bytes_used:=0; NEW.usage_measured_at:=now(); NEW.usage_error:=NULL; END IF;
 IF NEW.quota_bytes=0 THEN
  SELECT (value#>>'{}')::bigint INTO configured FROM settings WHERE scope='tenant' AND scope_id=tid AND key='mailbox_default_quota_bytes';
  IF configured IS NULL THEN SELECT (value#>>'{}')::bigint INTO configured FROM settings WHERE scope='platform' AND scope_id=0 AND key='mailbox_default_quota_bytes'; END IF;
  configured:=COALESCE(configured,268435456);
  IF configured<=0 THEN RAISE EXCEPTION 'INVALID_DEFAULT_QUOTA' USING ERRCODE='23514'; END IF;
  NEW.quota_bytes:=configured;
 END IF;
 IF NEW.quota_bytes=0 THEN RAISE EXCEPTION 'STORAGE_QUOTA_EXCEEDED' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' AND (NEW.quota_bytes<>-1 AND (OLD.quota_bytes=-1 OR NEW.quota_bytes<OLD.quota_bytes)) THEN
  IF OLD.usage_measured_at IS NULL OR OLD.usage_error IS NOT NULL OR OLD.usage_measured_at<now()-interval '10 minutes' THEN RAISE EXCEPTION 'STORAGE_USAGE_UNKNOWN' USING ERRCODE='23514'; END IF;
  IF NEW.quota_bytes<OLD.bytes_used THEN RAISE EXCEPTION 'STORAGE_BELOW_USAGE' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;

DO $$
DECLARE definition text; patched text;
BEGIN
 definition:=pg_get_functiondef('urmail_enqueue_verified_domain()'::regprocedure);
 patched:=replace(definition, ' AND EXISTS(SELECT 1 FROM subscriptions WHERE tenant_id=NEW.tenant_id AND status IN(''active'',''trialing'',''past_due'') AND period_start<=now() AND (period_end>now() OR urmail_migration_entitlement_allowed(NEW.tenant_id)))', '');
 IF patched=definition OR position('subscriptions' IN patched)>0 THEN RAISE EXCEPTION 'ENQUEUE_SUBSCRIPTION_GATE_NOT_REMOVED'; END IF;
 EXECUTE patched;
 definition:=pg_get_functiondef('urmail_request_onboarding(bigint,bigint,text)'::regprocedure);
 patched:=replace(definition, ' OR NOT EXISTS(SELECT 1 FROM subscriptions WHERE tenant_id=p_tenant AND status IN(''active'',''trialing'',''past_due'') AND period_start<=now() AND (period_end>now() OR urmail_migration_entitlement_allowed(p_tenant)))', '');
 IF patched=definition OR position('subscriptions' IN patched)>0 THEN RAISE EXCEPTION 'ONBOARDING_SUBSCRIPTION_GATE_NOT_REMOVED'; END IF;
 EXECUTE patched;
END $$;

CREATE OR REPLACE FUNCTION urmail_quota_day(p_tenant bigint) RETURNS date
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tz text;
BEGIN
 PERFORM pg_advisory_xact_lock(p_tenant);
 SELECT urmail_business_timezone(p_tenant) INTO tz;
 IF NOT EXISTS(SELECT 1 FROM pg_timezone_names WHERE name=tz) THEN RAISE EXCEPTION 'INVALID_TIMEZONE' USING ERRCODE='23514'; END IF;
 RETURN (now() AT TIME ZONE tz)::date;
END $$;

CREATE OR REPLACE FUNCTION urmail_take_mailbox_quota(p_tenant bigint, p_mailbox bigint) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE day_key date; updated integer;
BEGIN
 PERFORM pg_advisory_xact_lock(p_tenant);
 IF NOT urmail_mail_allowed(p_tenant) OR NOT EXISTS(SELECT 1 FROM mailboxes m JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE m.tenant_id=p_tenant AND m.id=p_mailbox AND m.status='active' AND m.send_enabled AND d.status='active' AND d.provisioning_state='ready') THEN RETURN false; END IF;
 day_key:=urmail_quota_day(p_tenant);
 INSERT INTO mailbox_usage(mailbox_id,tenant_id,day,messages_sent) VALUES(p_mailbox,p_tenant,day_key,1)
 ON CONFLICT(mailbox_id,day) DO UPDATE SET messages_sent=mailbox_usage.messages_sent+1
 WHERE mailbox_usage.messages_sent<2147483647 RETURNING messages_sent INTO updated;
 RETURN updated IS NOT NULL;
END $$;

CREATE OR REPLACE FUNCTION urmail_migration_entitlement_allowed(p_tenant bigint) RETURNS boolean
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 RETURN false;
END $$;

DROP TRIGGER IF EXISTS tenant_default_subscription ON tenants;
DROP FUNCTION IF EXISTS urmail_default_subscription();

DO $$
DECLARE r record;
BEGIN
 FOR r IN
  SELECT DISTINCT p.oid::regprocedure AS sig
  FROM pg_depend d
  JOIN pg_proc p ON p.oid=d.objid
  JOIN pg_type ty ON ty.oid=d.refobjid AND ty.typtype='c'
  JOIN pg_class c ON c.oid=ty.typrelid
  JOIN pg_namespace ns ON ns.oid=c.relnamespace
  WHERE ns.nspname='public' AND c.relname IN (
   'orders','order_items','payments','payment_events','invoices','coupons','coupon_redemptions','refunds',
   'plan_prices','billing_quotes','subscription_terms','subscription_changes','payment_manual_reviews',
   'payment_checkout_intents','payment_merchants','payment_webhook_inbox','payment_webhook_rejections',
   'payment_webhook_rejection_budget','billing_order_authorizations','billing_calendars',
   'commission_refund_adjustments','subscription_renewal_attempts','payment_recurring_capabilities',
   'subscription_renewal_authorizations','subscription_renewal_enrollments','recurring_setup_intents',
   'tenant_trials','registration_requests','tenant_contacts','plans','subscriptions','tenant_quota_calendar')
 LOOP
  EXECUTE 'DROP FUNCTION '||r.sig||' CASCADE';
 END LOOP;
END $$;

ALTER TABLE commissions DROP CONSTRAINT IF EXISTS commissions_order_tenant_fk;
ALTER TABLE commissions DROP CONSTRAINT IF EXISTS commission_source_payment_fk;
ALTER TABLE onboarding_tasks DROP CONSTRAINT IF EXISTS onboarding_tasks_order_id_fkey;

DROP TABLE IF EXISTS recurring_setup_intents CASCADE;
DROP TABLE IF EXISTS subscription_renewal_attempts CASCADE;
DROP TABLE IF EXISTS subscription_renewal_enrollments CASCADE;
DROP TABLE IF EXISTS subscription_renewal_authorizations CASCADE;
DROP TABLE IF EXISTS payment_recurring_capabilities CASCADE;
DROP TABLE IF EXISTS payment_merchants CASCADE;
DROP TABLE IF EXISTS payment_webhook_inbox CASCADE;
DROP TABLE IF EXISTS payment_webhook_rejections CASCADE;
DROP TABLE IF EXISTS payment_webhook_rejection_budget CASCADE;
DROP TABLE IF EXISTS payment_manual_reviews CASCADE;
DROP TABLE IF EXISTS payment_checkout_intents CASCADE;
DROP TABLE IF EXISTS commission_refund_adjustments CASCADE;
DROP TABLE IF EXISTS billing_order_authorizations CASCADE;
DROP TABLE IF EXISTS subscription_changes CASCADE;
DROP TABLE IF EXISTS tenant_trials CASCADE;
DROP TABLE IF EXISTS subscription_terms CASCADE;
DROP TABLE IF EXISTS refunds CASCADE;
DROP TABLE IF EXISTS invoices CASCADE;
DROP TABLE IF EXISTS payments CASCADE;
DROP TABLE IF EXISTS payment_events CASCADE;
DROP TABLE IF EXISTS coupon_redemptions CASCADE;
DROP TABLE IF EXISTS coupons CASCADE;
DROP TABLE IF EXISTS order_items CASCADE;
DROP TABLE IF EXISTS orders CASCADE;
DROP TABLE IF EXISTS billing_quotes CASCADE;
DROP TABLE IF EXISTS plan_prices CASCADE;
DROP TABLE IF EXISTS billing_calendars CASCADE;
DROP TABLE IF EXISTS registration_requests CASCADE;
DROP TABLE IF EXISTS tenant_contacts CASCADE;
DROP TABLE IF EXISTS tenant_quota_calendar CASCADE;
DROP TABLE IF EXISTS subscriptions CASCADE;
DROP TABLE IF EXISTS plans CASCADE;

DROP FUNCTION IF EXISTS urmail_billing_history_requires_review(bigint, bigint);
DROP FUNCTION IF EXISTS urmail_coupon_bound_redemption();
DROP FUNCTION IF EXISTS urmail_coupon_redemption_capacity();
DROP FUNCTION IF EXISTS urmail_coupon_redemption_guard();
DROP FUNCTION IF EXISTS urmail_legacy_current_entitlement_end(bigint);
DROP FUNCTION IF EXISTS urmail_plan_guard();
DROP FUNCTION IF EXISTS urmail_redeem_coupon(text, bigint);
DROP FUNCTION IF EXISTS urmail_redeem_coupon_for_tenant(text, bigint, bigint);
DROP FUNCTION IF EXISTS urmail_redeem_trial(bigint, bigint, text, text);
DROP FUNCTION IF EXISTS urmail_refund_approval_guard();
DROP FUNCTION IF EXISTS urmail_subscription_change_guard();
DROP FUNCTION IF EXISTS urmail_trial_redemption_guard();
