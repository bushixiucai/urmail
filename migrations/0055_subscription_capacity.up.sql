CREATE FUNCTION urmail_plan_snapshot_fits(p_tenant bigint,p_snapshot jsonb) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT COALESCE((p_tenant=nullif(current_setting('urmail.tenant_id',true),'')::bigint OR current_setting('role',true) IN('urmail_platform','urmail_dovecot','urmail_migrate'))
 AND p.quota_bytes IS NOT NULL AND p.mailbox_limit IS NOT NULL AND p.domain_limit IS NOT NULL
 AND (p.domain_limit=-1 OR (SELECT count(*) FROM domains WHERE tenant_id=p_tenant)<=p.domain_limit)
 AND (p.mailbox_limit=-1 OR (SELECT count(*) FROM mailboxes WHERE tenant_id=p_tenant AND status<>'deleted')<=p.mailbox_limit)
 AND (p.quota_bytes=-1 OR (
 NOT EXISTS(SELECT 1 FROM mailboxes WHERE tenant_id=p_tenant AND (status<>'deleted' OR bytes_used>0 OR usage_measured_at IS NULL) AND quota_bytes<=0)
 AND (SELECT COALESCE(sum(quota_bytes),0) FROM mailboxes WHERE tenant_id=p_tenant AND (status<>'deleted' OR bytes_used>0 OR usage_measured_at IS NULL))<=p.quota_bytes)),false)
 FROM jsonb_populate_record(NULL::plans,p_snapshot) p;
$$;
ALTER FUNCTION urmail_plan_snapshot_fits(bigint,jsonb) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_plan_snapshot_fits(bigint,jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_plan_snapshot_fits(bigint,jsonb) TO urmail_app,urmail_dovecot;
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure) INTO definition;
 definition:=replace(definition,'s.period_end>now()','s.period_end>now() AND /* capacity_activation_gate */ (NOT EXISTS(SELECT 1 FROM subscription_terms ct WHERE ct.tenant_id=s.tenant_id AND ct.basis_order_id IS NULL AND ct.revoked_at IS NULL AND ct.period_start<=now() AND ct.period_end>now()) OR EXISTS(SELECT 1 FROM urmail_effective_plan(s.tenant_id) ep WHERE urmail_plan_snapshot_fits(s.tenant_id,to_jsonb(ep))))');
 IF strpos(definition,'capacity_activation_gate')=0 THEN RAISE EXCEPTION 'MAIL_CAPACITY_GATE_UPDATE_REQUIRED'; END IF;
 EXECUTE definition;
 SELECT pg_get_functiondef('urmail_subscription_change_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'RETURN NEW;',
 'IF NOT urmail_plan_snapshot_fits(NEW.tenant_id,to_jsonb(p)) THEN RAISE EXCEPTION ''PLAN_CAPACITY_EXCEEDED'' USING ERRCODE=''23514''; END IF;
 IF p.code<>COALESCE((SELECT code FROM urmail_effective_plan(NEW.tenant_id)),s.plan_id) AND NOT(COALESCE(p.features->''downgrade_from'',''[]''::jsonb) ? COALESCE((SELECT code FROM urmail_effective_plan(NEW.tenant_id)),s.plan_id) OR COALESCE(p.features->''next_period_from'',''[]''::jsonb) ? COALESCE((SELECT code FROM urmail_effective_plan(NEW.tenant_id)),s.plan_id)) THEN RAISE EXCEPTION ''PLAN_CHANGE_REVIEW_REQUIRED'' USING ERRCODE=''23514''; END IF;
 RETURN NEW;');
 EXECUTE definition;
END $$;

CREATE OR REPLACE FUNCTION urmail_subscription_change_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE p plans%ROWTYPE; s subscriptions%ROWTYPE; actor bigint; source_code text;
BEGIN
 PERFORM pg_advisory_xact_lock(NEW.tenant_id);
 IF TG_OP='UPDATE' AND current_setting('role',true) IN('urmail_platform','urmail_migrate') AND NEW.target_plan_code=OLD.target_plan_code AND NEW.effective_at=OLD.effective_at AND NEW.requested_by=OLD.requested_by AND NEW.status IN('completed','blocked') AND NEW.effective_at<=now() AND EXISTS(SELECT 1 FROM subscription_terms t WHERE t.tenant_id=NEW.tenant_id AND t.period_start<=now() AND t.period_end>now() AND t.revoked_at IS NULL AND t.plan_snapshot->>'code'=NEW.target_plan_code) THEN
  IF NEW.status IS DISTINCT FROM OLD.status THEN INSERT INTO audit_logs(tenant_id,action,target_type,target_id,detail) VALUES(NEW.tenant_id,'APPLY subscription.change','subscription_changes',NEW.tenant_id::text,jsonb_build_object('actor_type','worker','requested_by',NEW.requested_by,'outcome',NEW.status,'status',200)); END IF;
  RETURN NEW;
 END IF;
 actor:=nullif(current_setting('urmail.user_id',true),'')::bigint;
 IF NEW.tenant_id IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint OR NEW.requested_by<>actor OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=NEW.tenant_id AND user_id=actor AND role IN('owner','admin','billing')) THEN RAISE EXCEPTION 'FORBIDDEN' USING ERRCODE='42501'; END IF;
 IF NEW.status='canceled' THEN
  IF EXISTS(SELECT 1 FROM subscription_terms t WHERE t.tenant_id=NEW.tenant_id AND t.basis_order_id IS NULL AND t.period_start>=NEW.effective_at AND t.plan_snapshot->>'code'=NEW.target_plan_code AND t.revoked_at IS NULL) THEN RAISE EXCEPTION 'PLAN_CHANGE_ALREADY_PAID' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF NEW.status<>'scheduled' THEN RAISE EXCEPTION 'FORBIDDEN' USING ERRCODE='42501'; END IF;
 SELECT * INTO s FROM subscriptions WHERE tenant_id=NEW.tenant_id AND status IN('active','trialing','past_due');
 IF NOT FOUND OR s.period_end<=now() OR NEW.effective_at<>s.period_end THEN RAISE EXCEPTION 'SUBSCRIPTION_CHANGED' USING ERRCODE='23514'; END IF;
 SELECT * INTO p FROM plans WHERE code=NEW.target_plan_code AND active;
 IF NOT FOUND THEN RAISE EXCEPTION 'PLAN_NOT_FOUND' USING ERRCODE='23514'; END IF;
 IF NOT urmail_plan_snapshot_fits(NEW.tenant_id,to_jsonb(p)) THEN RAISE EXCEPTION 'PLAN_CAPACITY_EXCEEDED' USING ERRCODE='23514'; END IF;
 SELECT COALESCE((SELECT t.plan_snapshot->>'code' FROM subscription_terms t WHERE t.tenant_id=NEW.tenant_id AND t.basis_order_id IS NULL AND t.period_start>now() AND t.revoked_at IS NULL ORDER BY t.period_end DESC,t.order_id DESC LIMIT 1),(SELECT code FROM urmail_effective_plan(NEW.tenant_id)),s.plan_id) INTO source_code;
 IF p.code<>source_code AND NOT(COALESCE(jsonb_typeof(p.features->'downgrade_from')='array' AND p.features->'downgrade_from' ? source_code,false) OR COALESCE(jsonb_typeof(p.features->'next_period_from')='array' AND p.features->'next_period_from' ? source_code,false)) THEN RAISE EXCEPTION 'PLAN_CHANGE_REVIEW_REQUIRED' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
