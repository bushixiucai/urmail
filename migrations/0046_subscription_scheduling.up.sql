CREATE TABLE subscription_changes (
 tenant_id bigint PRIMARY KEY REFERENCES tenants(id),
 target_plan_code text NOT NULL REFERENCES plans(code),
 effective_at timestamptz NOT NULL,
 status text NOT NULL DEFAULT 'scheduled' CHECK(status IN('scheduled','completed','canceled','blocked')),
 requested_by bigint NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE subscription_changes OWNER TO urmail_platform;
REVOKE ALL ON subscription_changes FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE subscription_changes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON subscription_changes
 USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint)
 WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
GRANT SELECT,INSERT,UPDATE ON subscription_changes TO urmail_app;
CREATE FUNCTION urmail_subscription_change_guard() RETURNS trigger
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
ALTER FUNCTION urmail_subscription_change_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_subscription_change_guard() FROM PUBLIC;
CREATE TRIGGER subscription_change_guard BEFORE INSERT OR UPDATE ON subscription_changes FOR EACH ROW EXECUTE FUNCTION urmail_subscription_change_guard();
