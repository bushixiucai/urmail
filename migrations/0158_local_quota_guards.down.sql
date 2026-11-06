DROP FUNCTION IF EXISTS urmail_begin_marketing_flow_send(bigint,bigint,text,text,bigint,bigint);
DROP FUNCTION IF EXISTS urmail_authorize_marketing_send(bigint,bigint,text,bigint);
DROP FUNCTION IF EXISTS urmail_schedule_marketing(bigint,bigint,timestamptz);
DROP FUNCTION IF EXISTS urmail_marketing_limits(bigint);

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

DROP FUNCTION IF EXISTS urmail_setting_limit(bigint, text);

-- Restore 0156's deny-all entitlement predicate.
CREATE OR REPLACE FUNCTION urmail_migration_entitlement_allowed(p_tenant bigint) RETURNS boolean
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 RETURN false;
END $$;
