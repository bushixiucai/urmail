-- Preserve explicitly reviewed legacy service without inventing a paid term.
ALTER TABLE subscriptions ADD COLUMN entitlement_source text NOT NULL DEFAULT 'standard',
 ADD COLUMN migration_manifest_sha256 text;
ALTER TABLE subscriptions ALTER COLUMN period_end DROP NOT NULL;
ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_entitlement_source_check CHECK (
 (entitlement_source='standard' AND migration_manifest_sha256 IS NULL AND period_end IS NOT NULL AND isfinite(period_start) AND isfinite(period_end))
 OR (entitlement_source='migration_legacy' AND migration_manifest_sha256 IS NOT NULL AND migration_manifest_sha256 ~ '^[a-f0-9]{64}$'
  AND plan_id='migration_legacy' AND period_end IS NULL AND isfinite(period_start)
  AND status IN('active','canceled') AND NOT cancel_at_period_end AND trial_ends_at IS NULL));
CREATE UNIQUE INDEX subscription_migration_manifest_unique ON subscriptions(migration_manifest_sha256)
 WHERE entitlement_source='migration_legacy';
INSERT INTO plans(code,name,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features,active)
VALUES('migration_legacy','{"en":"Preserved legacy service","zh-CN":"存量服务保留"}',0,'CNY',-1,-1,-1,-1,'{"default_mailbox_quota_bytes":-1}',false);
CREATE FUNCTION urmail_migration_plan_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF TG_OP='DELETE' OR NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'MIGRATION_PLAN_IMMUTABLE' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER migration_plan_guard BEFORE UPDATE OR DELETE ON plans
 FOR EACH ROW WHEN(OLD.code='migration_legacy') EXECUTE FUNCTION urmail_migration_plan_guard();

CREATE FUNCTION urmail_migration_entitlement_allowed(p_tenant bigint) RETURNS boolean
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint
 AND NOT (current_setting('role',true) IN('urmail_platform','urmail_migrate','urmail_dovecot')
  OR (current_setting('role',true)='none' AND (pg_has_role(session_user,'urmail_platform','USAGE') OR pg_has_role(session_user,'urmail_dovecot','USAGE')))) THEN
  RETURN false;
 END IF;
 RETURN EXISTS(SELECT 1 FROM subscriptions s WHERE s.tenant_id=p_tenant AND s.entitlement_source='migration_legacy'
  AND s.status='active' AND s.plan_id='migration_legacy' AND s.period_start<=now() AND s.period_end IS NULL
  AND s.migration_manifest_sha256 ~ '^[a-f0-9]{64}$' AND s.plan_snapshot->>'code'='migration_legacy');
END $$;
ALTER FUNCTION urmail_migration_entitlement_allowed(bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_migration_entitlement_allowed(bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_migration_entitlement_allowed(bigint) TO urmail_app,urmail_dovecot;

CREATE FUNCTION urmail_migration_subscription_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.entitlement_source='migration_legacy' THEN RAISE EXCEPTION 'MIGRATION_FACTS_IMMUTABLE' USING ERRCODE='23514'; END IF;
  RETURN OLD;
 END IF;
 IF NEW.entitlement_source='migration_legacy' OR (TG_OP='UPDATE' AND OLD.entitlement_source='migration_legacy') THEN
  IF NOT (current_setting('role',true) IN('urmail_platform','urmail_migrate')
   OR (current_setting('role',true)='none' AND pg_has_role(session_user,'urmail_platform','USAGE'))) THEN
   RAISE EXCEPTION 'MIGRATION_ENTITLEMENT_PLATFORM_REQUIRED' USING ERRCODE='42501';
  END IF;
  IF TG_OP='UPDATE' AND OLD.entitlement_source='migration_legacy' AND
    ((to_jsonb(NEW)-'status') IS DISTINCT FROM (to_jsonb(OLD)-'status') OR
      NOT(NEW.status=OLD.status OR (OLD.status='active' AND NEW.status='canceled'))) THEN
   RAISE EXCEPTION 'MIGRATION_FACTS_IMMUTABLE' USING ERRCODE='23514';
  END IF;
 END IF;
 IF NEW.plan_id='migration_legacy' AND NEW.entitlement_source<>'migration_legacy' THEN
  RAISE EXCEPTION 'MIGRATION_ENTITLEMENT_SOURCE_REQUIRED' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_migration_subscription_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_migration_subscription_guard() FROM PUBLIC;
CREATE TRIGGER b_migration_subscription_guard BEFORE INSERT OR UPDATE OR DELETE ON subscriptions
 FOR EACH ROW EXECUTE FUNCTION urmail_migration_subscription_guard();

CREATE FUNCTION urmail_grant_migration_entitlement(p_tenant bigint,p_manifest text,p_actor bigint,p_reason text)
RETURNS subscriptions LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE previous subscriptions%ROWTYPE; created subscriptions%ROWTYPE;
BEGIN
 IF NOT(current_setting('role',true) IN('urmail_platform','urmail_migrate')
  OR (current_setting('role',true)='none' AND pg_has_role(session_user,'urmail_platform','USAGE')))
 THEN RAISE EXCEPTION 'MIGRATION_ENTITLEMENT_PLATFORM_REQUIRED' USING ERRCODE='42501'; END IF;
 IF p_manifest IS NULL OR p_manifest !~ '^[a-f0-9]{64}$' OR p_reason IS NULL OR length(trim(p_reason)) NOT BETWEEN 20 AND 2000
 OR p_actor IS NULL OR NOT EXISTS(SELECT 1 FROM platform_admins WHERE user_id=p_actor)
 THEN RAISE EXCEPTION 'MIGRATION_ENTITLEMENT_PROOF_REQUIRED' USING ERRCODE='23514'; END IF;
 PERFORM pg_advisory_xact_lock(p_tenant);
 SELECT * INTO previous FROM subscriptions WHERE migration_manifest_sha256=p_manifest AND entitlement_source='migration_legacy';
 IF FOUND THEN
  IF previous.tenant_id<>p_tenant THEN RAISE EXCEPTION 'MIGRATION_MANIFEST_ALREADY_USED' USING ERRCODE='23514'; END IF;
  -- Replaying an imported manifest never reactivates an old grant after purchase.
  RETURN previous;
 END IF;
 IF NOT EXISTS(SELECT 1 FROM tenants WHERE id=p_tenant AND status IN('active','pending'))
 OR EXISTS(SELECT 1 FROM subscriptions WHERE tenant_id=p_tenant AND (entitlement_source='migration_legacy' OR plan_id<>'free'))
 OR EXISTS(SELECT 1 FROM orders WHERE tenant_id=p_tenant AND status IN('paid','partially_refunded','refunded'))
 OR EXISTS(SELECT 1 FROM subscription_terms WHERE tenant_id=p_tenant)
 OR EXISTS(SELECT 1 FROM billing_calendars WHERE tenant_id=p_tenant)
 OR EXISTS(SELECT 1 FROM tenant_trials WHERE tenant_id=p_tenant)
 OR EXISTS(SELECT 1 FROM subscription_changes WHERE tenant_id=p_tenant AND status IN('scheduled','blocked'))
 THEN RAISE EXCEPTION 'MIGRATION_ENTITLEMENT_CONFLICT' USING ERRCODE='23514'; END IF;
 UPDATE subscriptions SET status='canceled' WHERE tenant_id=p_tenant AND status IN('active','trialing','past_due');
 INSERT INTO subscriptions(tenant_id,plan_id,status,period_start,period_end,entitlement_source,migration_manifest_sha256,plan_snapshot)
 SELECT p_tenant,p.code,'active',now(),NULL,'migration_legacy',p_manifest,to_jsonb(p) FROM plans p WHERE p.code='migration_legacy'
 RETURNING * INTO created;
 IF created.id IS NULL THEN RAISE EXCEPTION 'MIGRATION_PLAN_UNAVAILABLE' USING ERRCODE='23514'; END IF;
 INSERT INTO audit_logs(tenant_id,actor_user_id,action,target_type,target_id,detail)
 VALUES(p_tenant,p_actor,'GRANT migration.entitlement','subscriptions',created.id::text,
  jsonb_build_object('status',200,'outcome','succeeded','reason',p_reason,'manifest_sha256',p_manifest,'entitlement_source','migration_legacy'));
 RETURN created;
END $$;
ALTER FUNCTION urmail_grant_migration_entitlement(bigint,text,bigint,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_grant_migration_entitlement(bigint,text,bigint,text) FROM PUBLIC,urmail_app,urmail_dovecot;
GRANT EXECUTE ON FUNCTION urmail_grant_migration_entitlement(bigint,text,bigint,text) TO urmail_migrate;

DO $$ DECLARE definition text; patched text; BEGIN
 SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure) INTO definition;
 patched:=replace(definition,'s.period_end>now()','(s.period_end>now() OR urmail_migration_entitlement_allowed(s.tenant_id))');
 IF patched=definition THEN RAISE EXCEPTION 'MIGRATION_MAIL_GATE_UPDATE_REQUIRED'; END IF; EXECUTE patched;
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 patched:=replace(definition,'s.period_end<=now()','COALESCE(s.period_end<=now(),NOT urmail_migration_entitlement_allowed(tid))');
 IF patched=definition THEN RAISE EXCEPTION 'MIGRATION_RESOURCE_GATE_UPDATE_REQUIRED'; END IF; EXECUTE patched;
 SELECT pg_get_functiondef('urmail_billing_history_requires_review(bigint,bigint)'::regprocedure) INTO definition;
 patched:=replace(definition,'s.plan_id<>''free''','s.plan_id<>''free'' AND s.entitlement_source<>''migration_legacy''');
 IF patched=definition THEN RAISE EXCEPTION 'MIGRATION_CALENDAR_GATE_UPDATE_REQUIRED'; END IF; EXECUTE patched;
 SELECT pg_get_functiondef('urmail_redeem_trial(bigint,bigint,text,text)'::regprocedure) INTO definition;
 patched:=replace(definition,'PERFORM pg_advisory_xact_lock(p_tenant);',
  'PERFORM pg_advisory_xact_lock(p_tenant); IF urmail_migration_entitlement_allowed(p_tenant) THEN RAISE EXCEPTION ''MIGRATION_ENTITLEMENT_EXISTS'' USING ERRCODE=''23514''; END IF;');
 IF patched=definition THEN RAISE EXCEPTION 'MIGRATION_TRIAL_GATE_UPDATE_REQUIRED'; END IF; EXECUTE patched;
 SELECT pg_get_functiondef('urmail_request_onboarding(bigint,bigint,text)'::regprocedure) INTO definition;
 patched:=replace(definition,'period_end>now()','(period_end>now() OR urmail_migration_entitlement_allowed(p_tenant))');
 IF patched=definition THEN RAISE EXCEPTION 'MIGRATION_ONBOARDING_GATE_UPDATE_REQUIRED'; END IF; EXECUTE patched;
 SELECT pg_get_functiondef('urmail_subscription_change_guard()'::regprocedure) INTO definition;
 patched:=replace(definition,'PERFORM pg_advisory_xact_lock(NEW.tenant_id);',
  'PERFORM pg_advisory_xact_lock(NEW.tenant_id); IF NEW.status<>''canceled'' AND urmail_migration_entitlement_allowed(NEW.tenant_id) THEN RAISE EXCEPTION ''MIGRATION_ENTITLEMENT_PRESERVED'' USING ERRCODE=''23514''; END IF;');
 IF patched=definition THEN RAISE EXCEPTION 'MIGRATION_SCHEDULE_GATE_UPDATE_REQUIRED'; END IF; EXECUTE patched;
 SELECT pg_get_functiondef('urmail_enqueue_verified_domain()'::regprocedure) INTO definition;
 patched:=replace(definition,'period_end>now()','(period_end>now() OR urmail_migration_entitlement_allowed(NEW.tenant_id))');
 IF patched=definition THEN RAISE EXCEPTION 'MIGRATION_ENQUEUE_GATE_UPDATE_REQUIRED'; END IF; EXECUTE patched;
END $$;
