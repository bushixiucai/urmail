-- Local quota after commercial tables are gone. Tenant settings win over
-- platform settings. Missing numeric limits are unlimited (-1). Zero rejects.
-- Marketing recipient limits stay out of the public settings whitelist.

-- Legacy mail facts survive without commercial tables: 0071's sieve guard
-- still demands this predicate before a legacy_v80 mailbox may be created.
-- A reviewed operator session (trusted role bound to the tenant) with an
-- active tenant counts as preserved authority; app sessions stay denied.
CREATE OR REPLACE FUNCTION urmail_migration_entitlement_allowed(p_tenant bigint) RETURNS boolean
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint
 AND NOT (current_setting('role',true) IN('urmail_platform','urmail_migrate','urmail_dovecot')
  OR (current_setting('role',true)='none' AND (pg_has_role(session_user,'urmail_platform','USAGE') OR pg_has_role(session_user,'urmail_dovecot','USAGE')))) THEN
  RETURN false;
 END IF;
 RETURN EXISTS(SELECT 1 FROM tenants WHERE id=p_tenant AND status='active');
END $$;

CREATE OR REPLACE FUNCTION urmail_setting_limit(p_tenant bigint, p_key text) RETURNS bigint
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT COALESCE(
  (SELECT (value#>>'{}')::bigint FROM settings WHERE scope='tenant' AND scope_id=p_tenant AND key=p_key),
  (SELECT (value#>>'{}')::bigint FROM settings WHERE scope='platform' AND scope_id=0 AND key=p_key),
  -1);
$$;
ALTER FUNCTION urmail_setting_limit(bigint,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_setting_limit(bigint,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_setting_limit(bigint,text) TO urmail_app,urmail_migrate;

CREATE OR REPLACE FUNCTION urmail_resource_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tid bigint; configured bigint; used bigint; lim bigint;
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
 IF TG_TABLE_NAME='domains' THEN
  IF TG_OP='INSERT' THEN
   SELECT count(*) INTO used FROM domains WHERE tenant_id=tid;
   lim:=urmail_setting_limit(tid,'mailbox_domain_limit');
   IF lim<>-1 AND used>=lim THEN RAISE EXCEPTION 'DOMAIN_QUOTA_EXCEEDED' USING ERRCODE='23514'; END IF;
  END IF;
  RETURN NEW;
 END IF;
 IF NOT urmail_domain_mail_ready(tid,NEW.domain_id) AND NOT (
  (current_setting('role',true) IN('urmail_platform','urmail_migrate')
   OR (current_setting('role',true)='none' AND pg_has_role(session_user,'urmail_platform','USAGE')))
 ) THEN RAISE EXCEPTION 'DOMAIN_NOT_PROVISIONED' USING ERRCODE='23514'; END IF;
 IF TG_OP='INSERT' OR (TG_OP='UPDATE' AND OLD.status='deleted' AND NEW.status<>'deleted') THEN
  SELECT count(*) INTO used FROM mailboxes WHERE tenant_id=tid AND status<>'deleted';
  lim:=urmail_setting_limit(tid,'mailbox_limit');
  IF lim<>-1 AND used>=lim THEN RAISE EXCEPTION 'MAILBOX_LIMIT_EXCEEDED' USING ERRCODE='23514'; END IF;
 END IF;
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
DECLARE day_key date; updated integer; lim bigint;
BEGIN
 PERFORM pg_advisory_xact_lock(p_tenant);
 IF NOT urmail_mail_allowed(p_tenant) OR NOT EXISTS(SELECT 1 FROM mailboxes m JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE m.tenant_id=p_tenant AND m.id=p_mailbox AND m.status='active' AND m.send_enabled AND d.status='active' AND d.provisioning_state='ready') THEN RETURN false; END IF;
 lim:=urmail_setting_limit(p_tenant,'mailbox_daily_send_limit');
 IF lim=0 THEN RETURN false; END IF;
 IF lim<0 OR lim>2147483647 THEN lim:=2147483647; END IF;
 day_key:=urmail_quota_day(p_tenant);
 INSERT INTO mailbox_usage(mailbox_id,tenant_id,day,messages_sent) VALUES(p_mailbox,p_tenant,day_key,1)
 ON CONFLICT(mailbox_id,day) DO UPDATE SET messages_sent=mailbox_usage.messages_sent+1
 WHERE mailbox_usage.messages_sent<lim RETURNING messages_sent INTO updated;
 RETURN updated IS NOT NULL;
END $$;

-- 0156 dropped these because they referenced plans. Recreate them against settings.
CREATE OR REPLACE FUNCTION urmail_marketing_limits(p_tenant bigint, OUT day_limit bigint, OUT rate_limit bigint)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE day_text text; rate_text text;
BEGIN
 SELECT COALESCE(
  (SELECT value#>>'{}' FROM settings WHERE scope='tenant' AND scope_id=p_tenant AND key='marketing_daily_recipient_limit'),
  (SELECT value#>>'{}' FROM settings WHERE scope='platform' AND scope_id=0 AND key='marketing_daily_recipient_limit')) INTO day_text;
 SELECT COALESCE(
  (SELECT value#>>'{}' FROM settings WHERE scope='tenant' AND scope_id=p_tenant AND key='marketing_rate_per_minute'),
  (SELECT value#>>'{}' FROM settings WHERE scope='platform' AND scope_id=0 AND key='marketing_rate_per_minute')) INTO rate_text;
 IF COALESCE(day_text,'') !~ '^[1-9][0-9]{0,8}$' OR COALESCE(rate_text,'') !~ '^[1-9][0-9]{0,5}$' THEN
  day_limit:=NULL; rate_limit:=NULL; RETURN;
 END IF;
 day_limit:=day_text::bigint; rate_limit:=rate_text::bigint;
END $$;
ALTER FUNCTION urmail_marketing_limits(bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_marketing_limits(bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_marketing_limits(bigint) TO urmail_app;

CREATE OR REPLACE FUNCTION urmail_schedule_marketing(p_tenant bigint,p_campaign bigint,p_at timestamptz) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE c marketing_campaigns%ROWTYPE; day_limit bigint; rate_limit bigint;
BEGIN
 IF p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=p_tenant AND user_id=nullif(current_setting('urmail.user_id',true),'')::bigint AND role IN('owner','admin')) THEN RAISE EXCEPTION 'MARKETING_FORBIDDEN' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(p_tenant);
 SELECT * INTO c FROM marketing_campaigns WHERE tenant_id=p_tenant AND id=p_campaign FOR UPDATE;
 IF NOT FOUND THEN RETURN 'not_found'; END IF;
 IF c.status<>'draft' THEN RETURN c.status; END IF;
 IF p_at IS NULL THEN RETURN 'invalid_schedule'; END IF;
 IF NOT urmail_mail_allowed(p_tenant) THEN RETURN 'entitlement_denied'; END IF;
 SELECT l.day_limit,l.rate_limit INTO day_limit,rate_limit FROM urmail_marketing_limits(p_tenant) l;
 IF day_limit IS NULL THEN RETURN 'quota_unconfigured'; END IF;
 IF NOT EXISTS(SELECT 1 FROM marketing_channels WHERE tenant_id=p_tenant AND id=c.channel_id AND enabled AND kind='dedicated_gateway') THEN RETURN 'channel_unavailable'; END IF;
 IF NOT EXISTS(SELECT 1 FROM marketing_channels ch JOIN mailboxes m ON m.tenant_id=ch.tenant_id AND m.address=ch.sender_address JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE ch.tenant_id=p_tenant AND ch.id=c.channel_id AND m.status='active' AND m.send_enabled AND d.status='active' AND d.provisioning_state='ready') THEN RETURN 'sender_unavailable'; END IF;
 UPDATE marketing_campaigns SET subject_snapshot=t.subject,body_snapshot=t.text_body,scheduled_at=p_at,status='scheduled' FROM marketing_templates t WHERE marketing_campaigns.id=c.id AND t.tenant_id=p_tenant AND t.id=c.template_id;
 INSERT INTO marketing_recipients(tenant_id,campaign_id,email)
 SELECT p_tenant,c.id,email FROM marketing_contacts mc WHERE mc.tenant_id=p_tenant AND mc.list_id=c.list_id AND mc.opted_in
 AND NOT EXISTS(SELECT 1 FROM marketing_suppressions s WHERE s.tenant_id=p_tenant AND s.email=mc.email)
 ON CONFLICT(campaign_id,email) DO NOTHING;
 RETURN 'scheduled';
END $$;
ALTER FUNCTION urmail_schedule_marketing(bigint,bigint,timestamptz) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_schedule_marketing(bigint,bigint,timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_schedule_marketing(bigint,bigint,timestamptz) TO urmail_app;

CREATE OR REPLACE FUNCTION urmail_authorize_marketing_send(p_tenant bigint,p_recipient bigint,p_owner text,p_fence bigint) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r marketing_recipients%ROWTYPE; c marketing_campaigns%ROWTYPE; w marketing_quota_windows%ROWTYPE; tz text; boundary timestamptz; start_time timestamptz; day_limit bigint; rate_limit bigint;
BEGIN
 IF current_setting('role',true)<>'urmail_platform' THEN RAISE EXCEPTION 'MARKETING_WORKER_REQUIRED' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(p_tenant);
 SELECT * INTO r FROM marketing_recipients WHERE tenant_id=p_tenant AND id=p_recipient FOR UPDATE;
 IF NOT FOUND OR r.status<>'leased' OR r.lease_owner IS DISTINCT FROM p_owner OR r.fence<>p_fence OR r.lease_until<=now() THEN RETURN 'stale'; END IF;
 SELECT * INTO c FROM marketing_campaigns WHERE tenant_id=p_tenant AND id=r.campaign_id;
 IF c.status NOT IN('scheduled','running') OR c.scheduled_at>now() THEN RETURN 'paused'; END IF;
 IF EXISTS(SELECT 1 FROM marketing_suppressions WHERE tenant_id=p_tenant AND email=r.email) OR NOT EXISTS(SELECT 1 FROM marketing_contacts WHERE tenant_id=p_tenant AND list_id=c.list_id AND email=r.email AND opted_in) THEN
  UPDATE marketing_recipients SET status='suppressed',completed_at=now(),result_code='suppressed' WHERE id=r.id;
  RETURN 'suppressed';
 END IF;
 IF NOT urmail_mail_allowed(p_tenant) THEN RETURN 'entitlement_denied'; END IF;
 SELECT l.day_limit,l.rate_limit INTO day_limit,rate_limit FROM urmail_marketing_limits(p_tenant) l;
 IF day_limit IS NULL THEN RETURN 'quota_unconfigured'; END IF;
 IF NOT EXISTS(SELECT 1 FROM marketing_channels WHERE tenant_id=p_tenant AND id=c.channel_id AND enabled AND kind='dedicated_gateway') THEN RETURN 'channel_unavailable'; END IF;
 IF NOT EXISTS(SELECT 1 FROM marketing_channels ch JOIN mailboxes m ON m.tenant_id=ch.tenant_id AND m.address=ch.sender_address JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE ch.tenant_id=p_tenant AND ch.id=c.channel_id AND m.status='active' AND m.send_enabled AND d.status='active' AND d.provisioning_state='ready') THEN RETURN 'sender_unavailable'; END IF;
 tz:=urmail_business_timezone(p_tenant);
 SELECT * INTO w FROM marketing_quota_windows WHERE tenant_id=p_tenant ORDER BY starts_at DESC LIMIT 1 FOR UPDATE;
 IF NOT FOUND OR w.ends_at<=now() THEN
  start_time:=now(); boundary:=(date_trunc('day',now() AT TIME ZONE tz)+interval '1 day') AT TIME ZONE tz;
  IF w.id IS NOT NULL AND w.timezone<>tz AND boundary<start_time+interval '24 hours' THEN
   boundary:=(date_trunc('day',(start_time+interval '24 hours') AT TIME ZONE tz)+interval '1 day') AT TIME ZONE tz;
  END IF;
  INSERT INTO marketing_quota_windows(tenant_id,starts_at,ends_at,timezone) VALUES(p_tenant,start_time,boundary,tz) RETURNING * INTO w;
 END IF;
 IF w.used_count>=day_limit THEN RETURN 'quota_exhausted'; END IF;
 IF w.next_send_at>now() THEN RETURN 'rate_limited'; END IF;
 UPDATE marketing_quota_windows SET used_count=used_count+1,next_send_at=now()+make_interval(secs=>60.0/rate_limit) WHERE id=w.id;
 UPDATE marketing_recipients SET status='submitting',window_id=w.id,reserved=true,attempts=attempts+1 WHERE id=r.id;
 UPDATE marketing_campaigns SET status='running' WHERE id=c.id;
 RETURN 'authorized';
END $$;
ALTER FUNCTION urmail_authorize_marketing_send(bigint,bigint,text,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_authorize_marketing_send(bigint,bigint,text,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_authorize_marketing_send(bigint,bigint,text,bigint) TO urmail_platform;

CREATE OR REPLACE FUNCTION urmail_begin_marketing_flow_send(p_tenant bigint,p_flow bigint,p_email text,p_owner text,p_fence bigint,p_channel bigint)
RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r marketing_flow_positions%ROWTYPE; w marketing_quota_windows%ROWTYPE;
 tz text; boundary timestamptz; start_time timestamptz; day_limit bigint; rate_limit bigint;
BEGIN
 IF current_setting('role',true)<>'urmail_platform' THEN RAISE EXCEPTION 'MARKETING_WORKER_REQUIRED' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(p_tenant);
 SELECT * INTO r FROM marketing_flow_positions WHERE tenant_id=p_tenant AND flow_id=p_flow AND email=p_email FOR UPDATE;
 IF NOT FOUND OR r.lease_owner IS DISTINCT FROM p_owner OR r.lease_fence<>p_fence OR r.lease_until IS NULL OR r.lease_until<=now() THEN RETURN 'stale_lease'; END IF;
 IF r.delivery_uncertain OR r.flow_reserved THEN RETURN 'uncertain'; END IF;
 IF r.stopped_at IS NOT NULL OR r.unsubscribed OR NOT EXISTS(SELECT 1 FROM marketing_flows WHERE tenant_id=p_tenant AND id=p_flow AND status='active') THEN RETURN 'paused'; END IF;
 IF EXISTS(SELECT 1 FROM marketing_suppressions WHERE tenant_id=p_tenant AND email=p_email)
 OR NOT EXISTS(SELECT 1 FROM marketing_flow_entries e JOIN marketing_contacts c ON c.tenant_id=e.tenant_id AND c.list_id=e.list_id AND c.email=p_email WHERE e.tenant_id=p_tenant AND e.id=r.entry_id AND c.opted_in) THEN RETURN 'suppressed'; END IF;
 IF NOT urmail_mail_allowed(p_tenant) THEN RETURN 'entitlement_denied'; END IF;
 SELECT l.day_limit,l.rate_limit INTO day_limit,rate_limit FROM urmail_marketing_limits(p_tenant) l;
 IF day_limit IS NULL THEN RETURN 'quota_unconfigured'; END IF;
 IF NOT EXISTS(SELECT 1 FROM marketing_channels WHERE tenant_id=p_tenant AND id=p_channel AND enabled AND kind='dedicated_gateway') THEN RETURN 'channel_unavailable'; END IF;
 IF NOT EXISTS(SELECT 1 FROM marketing_channels ch JOIN mailboxes m ON m.tenant_id=ch.tenant_id AND m.address=ch.sender_address JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE ch.tenant_id=p_tenant AND ch.id=p_channel AND m.status='active' AND m.send_enabled AND d.status='active' AND urmail_domain_mail_ready(m.tenant_id,m.domain_id)) THEN RETURN 'sender_unavailable'; END IF;
 tz:=urmail_business_timezone(p_tenant);
 SELECT * INTO w FROM marketing_quota_windows WHERE tenant_id=p_tenant ORDER BY starts_at DESC LIMIT 1 FOR UPDATE;
 IF NOT FOUND OR w.ends_at<=now() THEN
  start_time:=now();boundary:=(date_trunc('day',now() AT TIME ZONE tz)+interval '1 day') AT TIME ZONE tz;
  IF w.id IS NOT NULL AND w.timezone<>tz AND boundary<start_time+interval '24 hours' THEN boundary:=(date_trunc('day',(start_time+interval '24 hours') AT TIME ZONE tz)+interval '1 day') AT TIME ZONE tz; END IF;
  INSERT INTO marketing_quota_windows(tenant_id,starts_at,ends_at,timezone) VALUES(p_tenant,start_time,boundary,tz) RETURNING * INTO w;
 END IF;
 IF w.used_count>=day_limit THEN RETURN 'quota_exhausted'; END IF;
 IF w.next_send_at>now() THEN RETURN 'rate_limited'; END IF;
 UPDATE marketing_quota_windows SET used_count=used_count+1,next_send_at=now()+make_interval(secs=>60.0/rate_limit) WHERE tenant_id=p_tenant AND id=w.id;
 UPDATE marketing_flow_positions SET delivery_uncertain=true,flow_reserved=true,flow_window_id=w.id,updated_at=now() WHERE tenant_id=p_tenant AND id=r.id;
 RETURN 'authorized';
END $$;
ALTER FUNCTION urmail_begin_marketing_flow_send(bigint,bigint,text,text,bigint,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_begin_marketing_flow_send(bigint,bigint,text,text,bigint,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_begin_marketing_flow_send(bigint,bigint,text,text,bigint,bigint) TO urmail_platform;
