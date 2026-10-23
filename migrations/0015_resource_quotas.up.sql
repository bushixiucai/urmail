-- Existing unbounded allocations require explicit operator decisions.
DO $$ DECLARE n bigint; BEGIN
 SELECT count(*) INTO n FROM mailboxes m JOIN subscriptions s ON s.tenant_id=m.tenant_id AND s.status IN('active','trialing','past_due') JOIN plans p ON p.code=s.plan_id WHERE p.quota_bytes>=0 AND m.quota_bytes<=0;
 IF n>0 THEN RAISE EXCEPTION 'STORAGE_ALLOCATION_PREFLIGHT: % unbounded mailbox allocations in finite plans; run urmailctl data-check and review each identifier before migrating; no allocations changed',n; END IF;
END $$;
-- Effective mailbox allocations are persisted; zero is only an API default.
ALTER TABLE mailboxes DROP CONSTRAINT mailboxes_quota_bytes_check;
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_quota_bytes_check CHECK(quota_bytes>=-1);
ALTER TABLE mailboxes DROP CONSTRAINT mailboxes_status_check;
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_status_check CHECK(status IN('active','disabled','locked','deleted'));
ALTER TABLE mailboxes ADD COLUMN bytes_used bigint NOT NULL DEFAULT 0 CHECK(bytes_used>=0), ADD COLUMN usage_measured_at timestamptz, ADD COLUMN usage_error text;
ALTER TABLE plans ADD CONSTRAINT plans_limits_check CHECK(quota_bytes>=-1 AND mailbox_limit>=-1 AND domain_limit>=-1 AND daily_send_limit>=-1);
INSERT INTO plans(code,name,price_cents,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features) VALUES('free','{"en":"Free","zh-CN":"免费"}',0,1073741824,5,2,100,'{"default_mailbox_quota_bytes":268435456}') ON CONFLICT(code) DO NOTHING;
CREATE FUNCTION urmail_mail_allowed(p_tenant bigint) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT EXISTS(SELECT 1 FROM tenants t JOIN subscriptions s ON s.tenant_id=t.id WHERE t.id=p_tenant AND t.status='active' AND s.status IN('active','trialing','past_due') AND s.period_start<=now() AND s.period_end>now() AND (s.status<>'trialing' OR s.trial_ends_at IS NULL OR s.trial_ends_at>now()));
$$;
ALTER FUNCTION urmail_mail_allowed(bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_mail_allowed(bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_mail_allowed(bigint) TO urmail_app,urmail_dovecot;
CREATE FUNCTION urmail_default_subscription() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM plans WHERE code='free' AND active AND quota_bytes>0 AND mailbox_limit>0 AND domain_limit>0 AND daily_send_limit>0) THEN RAISE EXCEPTION 'DEFAULT_PLAN_UNAVAILABLE' USING ERRCODE='23514'; END IF;
 INSERT INTO subscriptions(tenant_id,plan_id,status,period_start,period_end) VALUES(NEW.id,'free','active',now(),now()+interval '100 years');
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_default_subscription() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_default_subscription() FROM PUBLIC;
CREATE TRIGGER tenant_default_subscription AFTER INSERT ON tenants FOR EACH ROW EXECUTE FUNCTION urmail_default_subscription();
CREATE FUNCTION urmail_resource_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tid bigint; p plans%ROWTYPE; s subscriptions%ROWTYPE; allocated numeric; used bigint; configured bigint; held boolean;
BEGIN
 tid:=COALESCE(NEW.tenant_id,OLD.tenant_id);
 PERFORM pg_advisory_xact_lock(tid);
 IF TG_OP='DELETE' THEN
  IF TG_TABLE_NAME='mailboxes' AND (OLD.status<>'deleted' OR OLD.usage_measured_at IS NULL OR OLD.usage_error IS NOT NULL OR OLD.usage_measured_at<now()-interval '10 minutes' OR OLD.bytes_used>0) THEN RAISE EXCEPTION 'STORAGE_NOT_EMPTY' USING ERRCODE='23514'; END IF;
  RETURN OLD;
 END IF;
 -- Pure cleanup/state/usage updates remain possible after expiration.
 IF TG_OP='UPDATE' THEN
  IF TG_TABLE_NAME='domains' AND NEW.tenant_id=OLD.tenant_id THEN RETURN NEW; END IF;
  IF TG_TABLE_NAME='mailboxes' AND NEW.tenant_id=OLD.tenant_id AND NEW.quota_bytes=OLD.quota_bytes AND NOT(OLD.status='deleted' AND NEW.status<>'deleted') THEN RETURN NEW; END IF;
 END IF;
 SELECT * INTO s FROM subscriptions WHERE tenant_id=tid AND status IN('active','trialing','past_due');
 IF NOT FOUND THEN RAISE EXCEPTION 'SUBSCRIPTION_REQUIRED' USING ERRCODE='23514'; END IF;
 SELECT * INTO p FROM plans WHERE code=s.plan_id;
 IF s.period_start>now() OR s.period_end<=now() OR (s.status='trialing' AND s.trial_ends_at<=now()) OR NOT EXISTS(SELECT 1 FROM tenants WHERE id=tid AND status IN('active','pending')) THEN RAISE EXCEPTION 'SUBSCRIPTION_EXPIRED' USING ERRCODE='23514'; END IF;
 IF TG_TABLE_NAME='domains' THEN
  SELECT count(*) INTO used FROM domains WHERE tenant_id=tid;
  IF p.domain_limit<>-1 AND used>=p.domain_limit THEN RAISE EXCEPTION 'DOMAIN_QUOTA_EXCEEDED' USING ERRCODE='23514'; END IF;
 ELSE
  IF TG_OP='INSERT' OR (TG_OP='UPDATE' AND OLD.status='deleted' AND NEW.status<>'deleted') THEN
   SELECT count(*) INTO used FROM mailboxes WHERE tenant_id=tid AND status<>'deleted';
   IF p.mailbox_limit<>-1 AND used>=p.mailbox_limit THEN RAISE EXCEPTION 'MAILBOX_LIMIT_EXCEEDED' USING ERRCODE='23514'; END IF;
   IF TG_OP='INSERT' THEN NEW.bytes_used:=0; NEW.usage_measured_at:=now(); NEW.usage_error:=NULL; END IF;
  END IF;
  IF NEW.quota_bytes=0 THEN
   SELECT (value#>>'{}')::bigint INTO configured FROM settings WHERE scope='tenant' AND scope_id=tid AND key='mailbox_default_quota_bytes';
   IF configured IS NULL THEN SELECT (value#>>'{}')::bigint INTO configured FROM settings WHERE scope='platform' AND scope_id=0 AND key='mailbox_default_quota_bytes'; END IF;
   configured:=COALESCE(configured,268435456);
   IF configured<=0 THEN RAISE EXCEPTION 'INVALID_DEFAULT_QUOTA' USING ERRCODE='23514'; END IF;
   NEW.quota_bytes:=CASE WHEN p.quota_bytes=-1 THEN configured ELSE LEAST(configured,p.quota_bytes) END;
  END IF;
  IF NEW.quota_bytes=0 THEN RAISE EXCEPTION 'STORAGE_QUOTA_EXCEEDED' USING ERRCODE='23514'; END IF;
  IF TG_OP='UPDATE' AND s.status='past_due' AND (NEW.quota_bytes=-1 OR NEW.quota_bytes>OLD.quota_bytes) THEN RAISE EXCEPTION 'SUBSCRIPTION_PAST_DUE' USING ERRCODE='23514'; END IF;
  IF TG_OP='UPDATE' AND (NEW.quota_bytes<>-1 AND (OLD.quota_bytes=-1 OR NEW.quota_bytes<OLD.quota_bytes)) THEN
   IF OLD.usage_measured_at IS NULL OR OLD.usage_error IS NOT NULL OR OLD.usage_measured_at<now()-interval '10 minutes' THEN RAISE EXCEPTION 'STORAGE_USAGE_UNKNOWN' USING ERRCODE='23514'; END IF;
   IF NEW.quota_bytes<OLD.bytes_used THEN RAISE EXCEPTION 'STORAGE_BELOW_USAGE' USING ERRCODE='23514'; END IF;
  END IF;
  IF TG_OP='INSERT' OR (TG_OP='UPDATE' AND (NEW.quota_bytes=-1 OR NEW.quota_bytes>OLD.quota_bytes)) THEN
   IF TG_OP='UPDATE' AND (OLD.usage_measured_at IS NULL OR OLD.usage_error IS NOT NULL OR OLD.usage_measured_at<now()-interval '10 minutes') THEN RAISE EXCEPTION 'STORAGE_USAGE_UNKNOWN' USING ERRCODE='23514'; END IF;
   IF EXISTS(SELECT 1 FROM mailboxes WHERE tenant_id=tid AND id<>COALESCE(NEW.id,0) AND (usage_measured_at IS NULL OR usage_error IS NOT NULL OR usage_measured_at<now()-interval '10 minutes')) THEN RAISE EXCEPTION 'STORAGE_USAGE_UNKNOWN' USING ERRCODE='23514'; END IF;
  END IF;
  IF p.quota_bytes<>-1 THEN
   IF NEW.quota_bytes=-1 THEN RAISE EXCEPTION 'STORAGE_QUOTA_EXCEEDED' USING ERRCODE='23514'; END IF;
   IF EXISTS(SELECT 1 FROM mailboxes WHERE tenant_id=tid AND id<>COALESCE(NEW.id,0) AND (status<>'deleted' OR bytes_used>0 OR usage_measured_at IS NULL) AND quota_bytes<=0) THEN RAISE EXCEPTION 'STORAGE_ALLOCATION_REVIEW_REQUIRED' USING ERRCODE='23514'; END IF;
   SELECT COALESCE(sum(quota_bytes),0) INTO allocated FROM mailboxes WHERE tenant_id=tid AND id<>COALESCE(NEW.id,0) AND (status<>'deleted' OR bytes_used>0 OR usage_measured_at IS NULL);
   IF allocated+NEW.quota_bytes>p.quota_bytes THEN RAISE EXCEPTION 'STORAGE_QUOTA_EXCEEDED' USING ERRCODE='23514'; END IF;
  END IF;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_resource_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_resource_guard() FROM PUBLIC;
CREATE TRIGGER domain_resource_guard BEFORE INSERT OR UPDATE ON domains FOR EACH ROW EXECUTE FUNCTION urmail_resource_guard();
CREATE TRIGGER mailbox_resource_guard BEFORE INSERT OR UPDATE OR DELETE ON mailboxes FOR EACH ROW EXECUTE FUNCTION urmail_resource_guard();
-- Usage is collected by the privileged local CLI, never by tenant requests.
REVOKE UPDATE ON mailboxes FROM urmail_app;
GRANT UPDATE(password_hash,quota_bytes,status,send_enabled,display_name,sieve_script) ON mailboxes TO urmail_app;
CREATE FUNCTION urmail_subscription_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE p plans%ROWTYPE; allocated numeric; n bigint;
BEGIN
 PERFORM pg_advisory_xact_lock(NEW.tenant_id);
 IF NEW.status NOT IN('active','trialing','past_due') THEN RETURN NEW; END IF;
 SELECT * INTO p FROM plans WHERE code=NEW.plan_id;
 IF p.quota_bytes<>-1 THEN
  IF EXISTS(SELECT 1 FROM mailboxes WHERE tenant_id=NEW.tenant_id AND quota_bytes<=0 AND (status<>'deleted' OR bytes_used>0 OR usage_measured_at IS NULL)) THEN RAISE EXCEPTION 'STORAGE_ALLOCATION_REVIEW_REQUIRED' USING ERRCODE='23514'; END IF;
  SELECT COALESCE(sum(quota_bytes),0) INTO allocated FROM mailboxes WHERE tenant_id=NEW.tenant_id AND (status<>'deleted' OR bytes_used>0 OR usage_measured_at IS NULL);
  IF allocated>p.quota_bytes THEN RAISE EXCEPTION 'STORAGE_QUOTA_EXCEEDED' USING ERRCODE='23514'; END IF;
 END IF;
 SELECT count(*) INTO n FROM domains WHERE tenant_id=NEW.tenant_id;
 IF p.domain_limit<>-1 AND n>p.domain_limit THEN RAISE EXCEPTION 'DOMAIN_QUOTA_EXCEEDED' USING ERRCODE='23514'; END IF;
 SELECT count(*) INTO n FROM mailboxes WHERE tenant_id=NEW.tenant_id AND status<>'deleted';
 IF p.mailbox_limit<>-1 AND n>p.mailbox_limit THEN RAISE EXCEPTION 'MAILBOX_LIMIT_EXCEEDED' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_subscription_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_subscription_guard() FROM PUBLIC;
CREATE TRIGGER subscription_resource_guard BEFORE INSERT OR UPDATE ON subscriptions FOR EACH ROW EXECUTE FUNCTION urmail_subscription_guard();
