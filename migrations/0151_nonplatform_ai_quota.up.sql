-- Explicit BYO/local software admission limits are independent of paid mail
-- entitlement facts. Existing finite-plan limits remain a compatible fallback.
ALTER TABLE ai_provider_configs
 ADD COLUMN nonplatform_call_limit bigint NOT NULL DEFAULT 0 CHECK(nonplatform_call_limit BETWEEN 0 AND 9007199254740991),
 ADD COLUMN nonplatform_mailbox_call_limit bigint NOT NULL DEFAULT 0 CHECK(nonplatform_mailbox_call_limit BETWEEN 0 AND 9007199254740991),
 ADD COLUMN nonplatform_concurrency_limit bigint NOT NULL DEFAULT 0 CHECK(nonplatform_concurrency_limit BETWEEN 0 AND 64),
 ADD COLUMN quota_timezone text NOT NULL DEFAULT '',
 ADD CONSTRAINT nonplatform_ai_quota_complete CHECK(
  (nonplatform_call_limit=0 AND nonplatform_mailbox_call_limit=0 AND nonplatform_concurrency_limit=0)
  OR (nonplatform_call_limit>0 AND nonplatform_mailbox_call_limit>0 AND nonplatform_concurrency_limit>0 AND quota_timezone<>''));

-- Freeze already configured provider budgets without changing their limits.
UPDATE ai_provider_configs SET quota_timezone=urmail_business_timezone(tenant_id)
WHERE daily_token_limit>0 OR daily_cost_micros>0 OR monthly_token_limit>0 OR monthly_cost_micros>0;

CREATE FUNCTION urmail_guard_nonplatform_ai_quota() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE changed boolean;
BEGIN
 IF TG_OP='UPDATE' AND OLD.quota_timezone<>'' AND NEW.quota_timezone IS DISTINCT FROM OLD.quota_timezone THEN
  RAISE EXCEPTION 'AI_QUOTA_TIMEZONE_FROZEN' USING ERRCODE='23514';
 END IF;
 IF NEW.quota_timezone='' AND (NEW.nonplatform_call_limit>0 OR NEW.daily_token_limit>0 OR NEW.daily_cost_micros>0 OR NEW.monthly_token_limit>0 OR NEW.monthly_cost_micros>0) THEN
  NEW.quota_timezone:=urmail_business_timezone(NEW.tenant_id);
 END IF;
 IF TG_OP='INSERT' THEN
  changed:=(NEW.nonplatform_call_limit,NEW.nonplatform_mailbox_call_limit,NEW.nonplatform_concurrency_limit,NEW.quota_timezone) IS DISTINCT FROM (0::bigint,0::bigint,0::bigint,''::text);
 ELSE
  changed:=(NEW.nonplatform_call_limit,NEW.nonplatform_mailbox_call_limit,NEW.nonplatform_concurrency_limit,NEW.quota_timezone) IS DISTINCT FROM (OLD.nonplatform_call_limit,OLD.nonplatform_mailbox_call_limit,OLD.nonplatform_concurrency_limit,OLD.quota_timezone);
 END IF;
 IF changed AND current_setting('role',true)<>'urmail_platform' THEN
  RAISE EXCEPTION 'AI_QUOTA_PLATFORM_REQUIRED' USING ERRCODE='42501';
 END IF;
 IF NEW.quota_timezone<>'' AND (NEW.quota_timezone='Local' OR NOT EXISTS(SELECT 1 FROM pg_timezone_names WHERE name=NEW.quota_timezone)) THEN
  RAISE EXCEPTION 'AI_QUOTA_TIMEZONE_INVALID' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_guard_nonplatform_ai_quota() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_guard_nonplatform_ai_quota() FROM PUBLIC;
CREATE TRIGGER nonplatform_ai_quota_guard BEFORE INSERT OR UPDATE ON ai_provider_configs FOR EACH ROW EXECUTE FUNCTION urmail_guard_nonplatform_ai_quota();

-- Keep quota admission bounded by the current tenant/window, and avoid
-- scanning years of completed requests to count active concurrency.
CREATE INDEX ai_usage_nonplatform_window_idx ON ai_usage_requests(tenant_id,created_at,mailbox_id)
 WHERE mode IN('byo','local') AND status<>'released';
CREATE INDEX ai_usage_active_reservations_idx ON ai_usage_requests(tenant_id,mode)
 WHERE status='reserved';
