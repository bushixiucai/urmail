CREATE TABLE tenant_trials (
 tenant_id bigint PRIMARY KEY REFERENCES tenants(id),
 domain_id bigint NOT NULL,
 coupon_code text NOT NULL REFERENCES coupons(code),
 plan_code text NOT NULL REFERENCES plans(code),
 plan_snapshot jsonb NOT NULL,
 started_at timestamptz NOT NULL DEFAULT now(),
 ends_at timestamptz NOT NULL,
 redeemed_by bigint NOT NULL REFERENCES users(id),
 FOREIGN KEY(tenant_id,domain_id) REFERENCES domains(tenant_id,id),
 CHECK(ends_at>started_at)
);
ALTER TABLE tenant_trials OWNER TO urmail_platform;
REVOKE ALL ON tenant_trials FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE tenant_trials ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_trials USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
GRANT SELECT ON tenant_trials TO urmail_app;
CREATE FUNCTION urmail_redeem_trial(p_tenant bigint,p_domain bigint,p_coupon text,p_plan text) RETURNS timestamptz
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE c coupons%ROWTYPE; p plans%ROWTYPE; actor bigint; finish timestamptz;
BEGIN
 actor:=nullif(current_setting('urmail.user_id',true),'')::bigint;
 IF p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=p_tenant AND user_id=actor AND role IN('owner','admin','billing')) THEN RAISE EXCEPTION 'FORBIDDEN' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(p_tenant);
 IF EXISTS(SELECT 1 FROM tenant_trials WHERE tenant_id=p_tenant) THEN RAISE EXCEPTION 'TRIAL_ALREADY_USED' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM domains WHERE tenant_id=p_tenant AND id=p_domain AND status='active' AND verified_at IS NOT NULL AND verified_at<=now()) THEN RAISE EXCEPTION 'DOMAIN_VERIFICATION_REQUIRED' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM subscriptions WHERE tenant_id=p_tenant AND plan_id<>'free' AND status IN('active','trialing','past_due') AND period_end>now()) OR EXISTS(SELECT 1 FROM subscription_terms WHERE tenant_id=p_tenant AND period_end>now()) THEN RAISE EXCEPTION 'PAID_ENTITLEMENT_EXISTS' USING ERRCODE='23514'; END IF;
 SELECT * INTO c FROM coupons WHERE code=p_coupon AND kind='trial_days' AND active AND value BETWEEN 1 AND 365 AND (valid_from IS NULL OR valid_from<=now()) AND (valid_to IS NULL OR valid_to>now()) FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'COUPON_INVALID' USING ERRCODE='23514'; END IF;
 SELECT * INTO p FROM plans WHERE code=p_plan AND active AND features->'trial_eligible'='true'::jsonb;
 IF NOT FOUND THEN RAISE EXCEPTION 'TRIAL_PLAN_UNAVAILABLE' USING ERRCODE='23514'; END IF;
 finish:=now()+make_interval(days=>c.value::integer);
 INSERT INTO tenant_trials(tenant_id,domain_id,coupon_code,plan_code,plan_snapshot,ends_at,redeemed_by) VALUES(p_tenant,p_domain,p_coupon,p_plan,to_jsonb(p),finish,actor);
 INSERT INTO coupon_redemptions(coupon_code,tenant_id,order_id) VALUES(p_coupon,p_tenant,NULL);
 UPDATE subscriptions SET status='canceled' WHERE tenant_id=p_tenant AND status IN('active','trialing','past_due');
 INSERT INTO subscriptions(tenant_id,plan_id,status,period_start,period_end,trial_ends_at,plan_snapshot) VALUES(p_tenant,p_plan,'trialing',now(),finish,finish,to_jsonb(p));
 INSERT INTO onboarding_tasks(tenant_id,domain_id,step) VALUES(p_tenant,p_domain,'dns_propagation');
 RETURN finish;
END $$;
ALTER FUNCTION urmail_redeem_trial(bigint,bigint,text,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_redeem_trial(bigint,bigint,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_redeem_trial(bigint,bigint,text,text) TO urmail_app;
CREATE FUNCTION urmail_trial_redemption_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  IF EXISTS(SELECT 1 FROM tenant_trials WHERE tenant_id=OLD.tenant_id AND coupon_code=OLD.coupon_code) THEN RAISE EXCEPTION 'TRIAL_REDEMPTION_IMMUTABLE' USING ERRCODE='23514'; END IF;
  RETURN OLD;
 END IF;
 IF NEW.order_id IS NULL AND NOT EXISTS(SELECT 1 FROM tenant_trials WHERE tenant_id=NEW.tenant_id AND coupon_code=NEW.coupon_code) THEN RAISE EXCEPTION 'COUPON_ORDER_REQUIRED' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_trial_redemption_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_trial_redemption_guard() FROM PUBLIC;
CREATE TRIGGER trial_redemption_guard BEFORE INSERT OR DELETE ON coupon_redemptions FOR EACH ROW EXECUTE FUNCTION urmail_trial_redemption_guard();
