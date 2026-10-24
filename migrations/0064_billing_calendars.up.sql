-- Use the same inherited business timezone shown by the tenant settings view.
CREATE FUNCTION urmail_business_timezone(p_tenant bigint) RETURNS text
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tz text;
BEGIN
 IF p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint
 AND NOT (current_setting('role',true) IN('urmail_platform','urmail_migrate','urmail_dovecot')
  OR (current_setting('role',true)='none' AND (pg_has_role(session_user,'urmail_platform','USAGE') OR pg_has_role(session_user,'urmail_dovecot','USAGE')))) THEN
  RAISE EXCEPTION 'TENANT_CONTEXT_REQUIRED' USING ERRCODE='42501';
 END IF;
 SELECT COALESCE(
  (SELECT value#>>'{}' FROM settings WHERE scope='tenant' AND scope_id=p_tenant AND key='business_timezone'),
  (SELECT s.value#>>'{}' FROM settings s JOIN tenants t ON t.brand_id=s.scope_id WHERE t.id=p_tenant AND s.scope='brand' AND s.key='business_timezone'),
  (SELECT value#>>'{}' FROM settings WHERE scope='platform' AND scope_id=0 AND key='business_timezone'),
  'Asia/Shanghai') INTO tz;
 IF tz='Local' OR NOT EXISTS(SELECT 1 FROM pg_timezone_names WHERE name=tz) THEN
  RAISE EXCEPTION 'INVALID_TIMEZONE' USING ERRCODE='23514';
 END IF;
 RETURN tz;
END $$;
ALTER FUNCTION urmail_business_timezone(bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_business_timezone(bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_business_timezone(bigint) TO urmail_app,urmail_dovecot;
DO $$
DECLARE signature regprocedure; definition text;
BEGIN
 FOREACH signature IN ARRAY ARRAY['urmail_quota_day(bigint)'::regprocedure,'urmail_authorize_marketing_send(bigint,bigint,text,bigint)'::regprocedure] LOOP
  definition:=pg_get_functiondef(signature);
  IF position('COALESCE((SELECT value#>>''{}'' FROM settings WHERE scope=''tenant'' AND scope_id=p_tenant AND key=''business_timezone''),''Asia/Shanghai'')' IN definition)=0 THEN
   RAISE EXCEPTION 'BUSINESS_TIMEZONE_UPGRADE_PRECONDITION';
  END IF;
  definition:=replace(definition,'COALESCE((SELECT value#>>''{}'' FROM settings WHERE scope=''tenant'' AND scope_id=p_tenant AND key=''business_timezone''),''Asia/Shanghai'')','urmail_business_timezone(p_tenant)');
  EXECUTE definition;
 END LOOP;
END $$;
-- One authoritative predicate for quote, settlement and operator inventory.
CREATE FUNCTION urmail_billing_history_requires_review(p_tenant bigint,p_excluded_order bigint) RETURNS boolean
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint
 AND NOT (current_setting('role',true) IN('urmail_platform','urmail_migrate')
  OR (current_setting('role',true)='none' AND pg_has_role(session_user,'urmail_platform','USAGE'))) THEN
  RAISE EXCEPTION 'TENANT_CONTEXT_REQUIRED' USING ERRCODE='42501';
 END IF;
 RETURN EXISTS(SELECT 1 FROM subscription_terms t WHERE t.tenant_id=p_tenant)
 OR EXISTS(SELECT 1 FROM orders o WHERE o.tenant_id=p_tenant AND o.id<>p_excluded_order AND o.status='paid')
 OR EXISTS(SELECT 1 FROM subscriptions s WHERE s.tenant_id=p_tenant AND s.plan_id<>'free'
  AND NOT EXISTS(SELECT 1 FROM tenant_trials tt WHERE tt.tenant_id=s.tenant_id AND tt.plan_code=s.plan_id AND tt.started_at=s.period_start AND tt.ends_at=s.period_end AND tt.ends_at=s.trial_ends_at));
END $$;
ALTER FUNCTION urmail_billing_history_requires_review(bigint,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_billing_history_requires_review(bigint,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_billing_history_requires_review(bigint,bigint) TO urmail_app;
-- Preserve the calendar facts of paid service independently of quota timezones.
CREATE TABLE billing_calendars (
 tenant_id bigint PRIMARY KEY REFERENCES tenants(id),
 timezone text NOT NULL,
 first_paid_at timestamptz NOT NULL,
 anchor_at timestamptz NOT NULL,
 anchor_wall jsonb NOT NULL,
 first_anchor_wall jsonb NOT NULL,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 source text NOT NULL CHECK(source IN('first_paid','manual_legacy','reactivation')),
 review_reason text NOT NULL,
 created_by bigint REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE billing_calendars OWNER TO urmail_platform;
REVOKE ALL ON billing_calendars FROM PUBLIC,urmail_app,urmail_dovecot,urmail_roundcube;
ALTER TABLE billing_calendars ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON billing_calendars USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
GRANT SELECT ON billing_calendars TO urmail_app;
CREATE FUNCTION urmail_billing_calendar_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.timezone='Local' OR NOT EXISTS(SELECT 1 FROM pg_timezone_names WHERE name=NEW.timezone) THEN
  RAISE EXCEPTION 'INVALID_BILLING_TIMEZONE' USING ERRCODE='23514';
 END IF;
 IF TG_OP='UPDATE' AND (NEW.timezone<>OLD.timezone OR NEW.first_paid_at<>OLD.first_paid_at
   OR NEW.tenant_id<>OLD.tenant_id OR NEW.created_by IS DISTINCT FROM OLD.created_by
   OR NEW.first_anchor_wall IS DISTINCT FROM OLD.first_anchor_wall
   OR NEW.anchor_at<=OLD.anchor_at OR NEW.version<>OLD.version+1 OR NEW.source<>'reactivation') THEN
  RAISE EXCEPTION 'BILLING_CALENDAR_IMMUTABLE' USING ERRCODE='23514';
 END IF;
 NEW.anchor_wall:=jsonb_build_object(
  'timezone',NEW.timezone,
  'day',extract(day FROM NEW.anchor_at AT TIME ZONE NEW.timezone)::int,
  'hour',extract(hour FROM NEW.anchor_at AT TIME ZONE NEW.timezone)::int,
  'minute',extract(minute FROM NEW.anchor_at AT TIME ZONE NEW.timezone)::int,
  'second',floor(extract(second FROM NEW.anchor_at AT TIME ZONE NEW.timezone))::int,
  'nanosecond',(mod(extract(microseconds FROM NEW.anchor_at AT TIME ZONE NEW.timezone),1000000)*1000)::int);
 IF TG_OP='INSERT' THEN NEW.first_anchor_wall:=NEW.anchor_wall; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_billing_calendar_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_billing_calendar_guard() FROM PUBLIC;
CREATE TRIGGER billing_calendar_guard BEFORE INSERT OR UPDATE ON billing_calendars FOR EACH ROW EXECUTE FUNCTION urmail_billing_calendar_guard();
ALTER TABLE subscription_terms ADD COLUMN calendar_snapshot jsonb;
-- Historical dates and unknown historical zones are deliberately not guessed.
