DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure) INTO definition;
 definition:=replace(definition,' AND /* capacity_activation_gate */ (NOT EXISTS(SELECT 1 FROM subscription_terms ct WHERE ct.tenant_id=s.tenant_id AND ct.basis_order_id IS NULL AND ct.revoked_at IS NULL AND ct.period_start<=now() AND ct.period_end>now()) OR EXISTS(SELECT 1 FROM urmail_effective_plan(s.tenant_id) ep WHERE urmail_plan_snapshot_fits(s.tenant_id,to_jsonb(ep))))','');
 EXECUTE definition;
 SELECT pg_get_functiondef('urmail_subscription_change_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'IF NOT urmail_plan_snapshot_fits(NEW.tenant_id,to_jsonb(p)) THEN RAISE EXCEPTION ''PLAN_CAPACITY_EXCEEDED'' USING ERRCODE=''23514''; END IF;
 IF p.code<>COALESCE((SELECT code FROM urmail_effective_plan(NEW.tenant_id)),s.plan_id) AND NOT(COALESCE(p.features->''downgrade_from'',''[]''::jsonb) ? COALESCE((SELECT code FROM urmail_effective_plan(NEW.tenant_id)),s.plan_id) OR COALESCE(p.features->''next_period_from'',''[]''::jsonb) ? COALESCE((SELECT code FROM urmail_effective_plan(NEW.tenant_id)),s.plan_id)) THEN RAISE EXCEPTION ''PLAN_CHANGE_REVIEW_REQUIRED'' USING ERRCODE=''23514''; END IF;
 ','');
 EXECUTE definition;
END $$;
CREATE OR REPLACE FUNCTION urmail_subscription_change_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE p plans%ROWTYPE; s subscriptions%ROWTYPE; actor bigint;
BEGIN
 actor:=nullif(current_setting('urmail.user_id',true),'')::bigint;
 IF NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=NEW.tenant_id AND user_id=actor AND role IN('owner','admin','billing')) OR NEW.requested_by<>actor THEN
  RAISE EXCEPTION 'FORBIDDEN' USING ERRCODE='42501';
 END IF;
 PERFORM pg_advisory_xact_lock(NEW.tenant_id);
 SELECT * INTO s FROM subscriptions WHERE tenant_id=NEW.tenant_id AND status IN('active','trialing','past_due');
 IF NOT FOUND OR NEW.effective_at<>s.period_end THEN RAISE EXCEPTION 'SUBSCRIPTION_CHANGED' USING ERRCODE='23514'; END IF;
 SELECT * INTO p FROM plans WHERE code=NEW.target_plan_code AND active;
 IF NOT FOUND THEN RAISE EXCEPTION 'PLAN_NOT_FOUND' USING ERRCODE='23514'; END IF;
 IF (p.quota_bytes>=0 AND (SELECT COALESCE(sum(quota_bytes),0) FROM mailboxes WHERE tenant_id=NEW.tenant_id AND status<>'deleted')>p.quota_bytes)
  OR (p.mailbox_limit>=0 AND (SELECT count(*) FROM mailboxes WHERE tenant_id=NEW.tenant_id AND status<>'deleted')>p.mailbox_limit)
  OR (p.domain_limit>=0 AND (SELECT count(*) FROM domains WHERE tenant_id=NEW.tenant_id)>p.domain_limit) THEN
   RAISE EXCEPTION 'PLAN_CAPACITY_EXCEEDED' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

DROP FUNCTION urmail_plan_snapshot_fits(bigint,jsonb);
