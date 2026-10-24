-- The date key is a stable usage-window identity; its boundary is persisted
-- separately so changing the business timezone cannot reset a live allowance.
CREATE TABLE tenant_quota_calendar (
 tenant_id bigint PRIMARY KEY REFERENCES tenants(id),
 usage_day date NOT NULL,
 timezone text NOT NULL,
 starts_at timestamptz NOT NULL,
 ends_at timestamptz NOT NULL CHECK(ends_at>starts_at)
);
ALTER TABLE tenant_quota_calendar OWNER TO urmail_platform;
REVOKE ALL ON tenant_quota_calendar FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE tenant_quota_calendar ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_quota_calendar USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
GRANT SELECT ON tenant_quota_calendar TO urmail_app;
CREATE FUNCTION urmail_quota_day(p_tenant bigint) RETURNS date
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE w tenant_quota_calendar%ROWTYPE; tz text; boundary timestamptz; beginning timestamptz;
BEGIN
 PERFORM pg_advisory_xact_lock(p_tenant);
 SELECT COALESCE((SELECT value#>>'{}' FROM settings WHERE scope='tenant' AND scope_id=p_tenant AND key='business_timezone'),'Asia/Shanghai') INTO tz;
 IF NOT EXISTS(SELECT 1 FROM pg_timezone_names WHERE name=tz) THEN RAISE EXCEPTION 'INVALID_TIMEZONE' USING ERRCODE='23514'; END IF;
 SELECT * INTO w FROM tenant_quota_calendar WHERE tenant_id=p_tenant FOR UPDATE;
 IF w.tenant_id IS NULL THEN
  beginning:=date_trunc('day',now() AT TIME ZONE tz) AT TIME ZONE tz;
  boundary:=((now() AT TIME ZONE tz)::date+1)::timestamp AT TIME ZONE tz;
  -- Carry the existing server-date counter on first activation; never grant
  -- another allowance merely because this calendar was introduced.
  INSERT INTO tenant_quota_calendar VALUES(p_tenant,current_date,tz,beginning,boundary) RETURNING * INTO w;
 ELSIF w.ends_at<=now() THEN
  beginning:=date_trunc('day',now() AT TIME ZONE tz) AT TIME ZONE tz;
  boundary:=((now() AT TIME ZONE tz)::date+1)::timestamp AT TIME ZONE tz;
  IF w.timezone<>tz THEN boundary:=GREATEST(boundary,w.ends_at+interval '24 hours'); END IF;
  UPDATE tenant_quota_calendar SET usage_day=GREATEST(w.usage_day+1,current_date),timezone=tz,starts_at=GREATEST(w.ends_at,beginning),ends_at=boundary WHERE tenant_id=p_tenant RETURNING * INTO w;
 END IF;
 RETURN w.usage_day;
END $$;
ALTER FUNCTION urmail_quota_day(bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_quota_day(bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_quota_day(bigint) TO urmail_platform;
CREATE FUNCTION urmail_take_mailbox_quota(p_tenant bigint,p_mailbox bigint) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE lim integer; day_key date; updated integer;
BEGIN
 PERFORM pg_advisory_xact_lock(p_tenant);
 IF NOT urmail_mail_allowed(p_tenant) OR NOT EXISTS(SELECT 1 FROM mailboxes m JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE m.tenant_id=p_tenant AND m.id=p_mailbox AND m.status='active' AND m.send_enabled AND d.status='active') THEN RETURN false; END IF;
 SELECT daily_send_limit INTO lim FROM urmail_effective_plan(p_tenant);
 IF lim IS NULL OR lim=0 THEN RETURN false; END IF;
 IF lim=-1 THEN lim:=2147483647; END IF;
 day_key:=urmail_quota_day(p_tenant);
 INSERT INTO mailbox_usage(mailbox_id,tenant_id,day,messages_sent) VALUES(p_mailbox,p_tenant,day_key,1)
 ON CONFLICT(mailbox_id,day) DO UPDATE SET messages_sent=mailbox_usage.messages_sent+1
 WHERE mailbox_usage.messages_sent<lim RETURNING messages_sent INTO updated;
 RETURN updated IS NOT NULL;
END $$;
ALTER FUNCTION urmail_take_mailbox_quota(bigint,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_take_mailbox_quota(bigint,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_take_mailbox_quota(bigint,bigint) TO urmail_platform;
