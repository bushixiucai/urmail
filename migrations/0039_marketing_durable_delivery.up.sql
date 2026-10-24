-- Durable marketing facts: tenant consent, immutable campaign content,
-- fenced attempts and window-bound reservations. No channel is enabled by default.
CREATE TABLE marketing_channels (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 kind text NOT NULL CHECK(kind IN('dedicated_gateway','dedicated_ip')),
 sender_address text NOT NULL CHECK(sender_address=lower(sender_address) AND sender_address !~ '[\r\n]'),
 encrypted_config text NOT NULL CHECK(encrypted_config LIKE 'enc:v1:%'),
 enabled boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(tenant_id,id)
);
CREATE TABLE marketing_templates (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 subject text NOT NULL CHECK(length(subject) BETWEEN 1 AND 998 AND subject !~ '[\r\n]'),
 text_body text NOT NULL CHECK(octet_length(text_body)<=1048576),
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(tenant_id,id)
);
CREATE TABLE marketing_lists (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(tenant_id,id)
);
CREATE TABLE marketing_contacts (
 tenant_id bigint NOT NULL REFERENCES tenants(id), list_id bigint NOT NULL,
 email text NOT NULL CHECK(email=lower(email) AND length(email)<=254 AND email !~ '[\r\n]'),
 consent_source text NOT NULL CHECK(length(consent_source) BETWEEN 1 AND 256),
 opted_in boolean NOT NULL DEFAULT true, consent_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,list_id,email),
 FOREIGN KEY(tenant_id,list_id) REFERENCES marketing_lists(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE marketing_campaigns (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 channel_id bigint NOT NULL, template_id bigint NOT NULL, list_id bigint NOT NULL,
 status text NOT NULL DEFAULT 'draft' CHECK(status IN('draft','scheduled','running','paused','canceled','completed')),
 subject_snapshot text, body_snapshot text,
 scheduled_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(tenant_id,id),
 FOREIGN KEY(tenant_id,channel_id) REFERENCES marketing_channels(tenant_id,id),
 FOREIGN KEY(tenant_id,template_id) REFERENCES marketing_templates(tenant_id,id),
 FOREIGN KEY(tenant_id,list_id) REFERENCES marketing_lists(tenant_id,id)
);
CREATE TABLE marketing_quota_windows (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 starts_at timestamptz NOT NULL, ends_at timestamptz NOT NULL CHECK(ends_at>starts_at),
 timezone text NOT NULL, used_count bigint NOT NULL DEFAULT 0 CHECK(used_count>=0),
 next_send_at timestamptz NOT NULL DEFAULT '-infinity',
 UNIQUE(tenant_id,starts_at), UNIQUE(tenant_id,id)
);
CREATE TABLE marketing_recipients (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id), campaign_id bigint NOT NULL,
 email text NOT NULL, status text NOT NULL DEFAULT 'queued'
 CHECK(status IN('queued','leased','submitting','sent','uncertain','failed','suppressed','canceled','bounced')),
 lease_owner text, lease_until timestamptz, fence bigint NOT NULL DEFAULT 0,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
 window_id bigint, reserved boolean NOT NULL DEFAULT false,
 result_code text, next_attempt_at timestamptz NOT NULL DEFAULT now(), created_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz,
 UNIQUE(tenant_id,id), UNIQUE(campaign_id,email),
 FOREIGN KEY(tenant_id,campaign_id) REFERENCES marketing_campaigns(tenant_id,id),
 FOREIGN KEY(tenant_id,window_id) REFERENCES marketing_quota_windows(tenant_id,id)
);
CREATE INDEX marketing_recipients_claim_idx ON marketing_recipients(status,lease_until,id);
CREATE TABLE marketing_feedback (
 channel_id bigint NOT NULL REFERENCES marketing_channels(id), event_id text NOT NULL,
 tenant_id bigint NOT NULL, recipient_id bigint NOT NULL, status text NOT NULL,
 payload_hash text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(channel_id,event_id),
 FOREIGN KEY(tenant_id,recipient_id) REFERENCES marketing_recipients(tenant_id,id)
);
DO $$ DECLARE n text; BEGIN
 FOREACH n IN ARRAY ARRAY['marketing_channels','marketing_templates','marketing_lists','marketing_contacts','marketing_campaigns','marketing_quota_windows','marketing_recipients','marketing_feedback'] LOOP
  EXECUTE format('ALTER TABLE %I OWNER TO urmail_platform',n);
  EXECUTE format('REVOKE ALL ON %I FROM PUBLIC,urmail_app,urmail_dovecot',n);
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',n);
  EXECUTE format('CREATE POLICY tenant_isolation ON %I USING(tenant_id=nullif(current_setting(''urmail.tenant_id'',true),'''')::bigint) WITH CHECK(tenant_id=nullif(current_setting(''urmail.tenant_id'',true),'''')::bigint)',n);
  EXECUTE format('GRANT SELECT ON %I TO urmail_app',n);
 END LOOP;
END $$;
REVOKE SELECT ON marketing_channels FROM urmail_app;
GRANT SELECT(id,tenant_id,name,kind,enabled,created_at) ON marketing_channels TO urmail_app;
GRANT INSERT(tenant_id,name,subject,text_body) ON marketing_templates TO urmail_app;
GRANT INSERT(tenant_id,name) ON marketing_lists TO urmail_app;
GRANT INSERT(tenant_id,name,channel_id,template_id,list_id) ON marketing_campaigns TO urmail_app;
GRANT INSERT,UPDATE ON marketing_contacts TO urmail_app;
GRANT UPDATE(status) ON marketing_campaigns TO urmail_app;
GRANT USAGE,SELECT ON SEQUENCE marketing_templates_id_seq,marketing_lists_id_seq,marketing_campaigns_id_seq TO urmail_app;

CREATE FUNCTION urmail_require_marketing_admin() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tid bigint;
BEGIN
 IF current_setting('role',true) IN('urmail_platform','urmail_migrate') THEN RETURN NEW; END IF;
 tid:=NEW.tenant_id;
 IF tid IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=tid AND user_id=nullif(current_setting('urmail.user_id',true),'')::bigint AND role IN('owner','admin')) THEN RAISE EXCEPTION 'MARKETING_FORBIDDEN' USING ERRCODE='42501'; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_require_marketing_admin() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_require_marketing_admin() FROM PUBLIC;
DO $$ DECLARE n text; BEGIN
 FOREACH n IN ARRAY ARRAY['marketing_templates','marketing_lists','marketing_contacts','marketing_campaigns'] LOOP
  EXECUTE format('CREATE TRIGGER marketing_admin_guard BEFORE INSERT OR UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION urmail_require_marketing_admin()',n);
 END LOOP;
END $$;

-- Scheduling is transactional and freezes content. A second confirmation
-- cannot rebuild recipient tasks or silently replace the campaign snapshot.
CREATE FUNCTION urmail_schedule_marketing(p_tenant bigint,p_campaign bigint,p_at timestamptz) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE c marketing_campaigns%ROWTYPE; p plans%ROWTYPE;
BEGIN
 IF p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=p_tenant AND user_id=nullif(current_setting('urmail.user_id',true),'')::bigint AND role IN('owner','admin')) THEN RAISE EXCEPTION 'MARKETING_FORBIDDEN' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(p_tenant);
 SELECT * INTO c FROM marketing_campaigns WHERE tenant_id=p_tenant AND id=p_campaign FOR UPDATE;
 IF NOT FOUND THEN RETURN 'not_found'; END IF;
 IF c.status<>'draft' THEN RETURN c.status; END IF;
 IF p_at IS NULL THEN RETURN 'invalid_schedule'; END IF;
 IF NOT urmail_mail_allowed(p_tenant) THEN RETURN 'entitlement_denied'; END IF;
 SELECT * INTO p FROM urmail_effective_plan(p_tenant);
 IF COALESCE(p.features->>'marketing_daily_recipient_limit','') !~ '^[1-9][0-9]{0,8}$' OR COALESCE(p.features->>'marketing_rate_per_minute','') !~ '^[1-9][0-9]{0,5}$' THEN RETURN 'quota_unconfigured'; END IF;
 IF NOT EXISTS(SELECT 1 FROM marketing_channels WHERE tenant_id=p_tenant AND id=c.channel_id AND enabled AND kind='dedicated_gateway') THEN RETURN 'channel_unavailable'; END IF;
 IF NOT EXISTS(SELECT 1 FROM marketing_channels ch JOIN mailboxes m ON m.tenant_id=ch.tenant_id AND m.address=ch.sender_address JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE ch.tenant_id=p_tenant AND ch.id=c.channel_id AND m.status='active' AND m.send_enabled AND d.status='active') THEN RETURN 'sender_unavailable'; END IF;
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

-- The final authorization is the unsubscribe/attempt linearization point.
-- No network call runs inside this transaction. In-flight SMTP cannot be recalled.
CREATE FUNCTION urmail_authorize_marketing_send(p_tenant bigint,p_recipient bigint,p_owner text,p_fence bigint) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r marketing_recipients%ROWTYPE; c marketing_campaigns%ROWTYPE; p plans%ROWTYPE; w marketing_quota_windows%ROWTYPE; tz text; boundary timestamptz; start_time timestamptz; day_limit bigint; rate_limit bigint;
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
 SELECT * INTO p FROM urmail_effective_plan(p_tenant);
 IF COALESCE(p.features->>'marketing_daily_recipient_limit','') !~ '^[1-9][0-9]{0,8}$' OR COALESCE(p.features->>'marketing_rate_per_minute','') !~ '^[1-9][0-9]{0,5}$' THEN RETURN 'quota_unconfigured'; END IF;
 day_limit:=(p.features->>'marketing_daily_recipient_limit')::bigint; rate_limit:=(p.features->>'marketing_rate_per_minute')::bigint;
 IF NOT EXISTS(SELECT 1 FROM marketing_channels WHERE tenant_id=p_tenant AND id=c.channel_id AND enabled AND kind='dedicated_gateway') THEN RETURN 'channel_unavailable'; END IF;
 IF NOT EXISTS(SELECT 1 FROM marketing_channels ch JOIN mailboxes m ON m.tenant_id=ch.tenant_id AND m.address=ch.sender_address JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE ch.tenant_id=p_tenant AND ch.id=c.channel_id AND m.status='active' AND m.send_enabled AND d.status='active') THEN RETURN 'sender_unavailable'; END IF;
 SELECT COALESCE((SELECT value#>>'{}' FROM settings WHERE scope='tenant' AND scope_id=p_tenant AND key='business_timezone'),'Asia/Shanghai') INTO tz;
 IF NOT EXISTS(SELECT 1 FROM pg_timezone_names WHERE name=tz) THEN RETURN 'timezone_invalid'; END IF;
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

CREATE FUNCTION urmail_finish_marketing_send(p_tenant bigint,p_recipient bigint,p_owner text,p_fence bigint,p_result text) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r marketing_recipients%ROWTYPE;
BEGIN
 IF current_setting('role',true)<>'urmail_platform' OR p_result NOT IN('sent','uncertain','failed','bounced') THEN RAISE EXCEPTION 'MARKETING_WORKER_REQUIRED' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(p_tenant);
 SELECT * INTO r FROM marketing_recipients WHERE tenant_id=p_tenant AND id=p_recipient FOR UPDATE;
 IF NOT FOUND OR r.status<>'submitting' OR r.lease_owner IS DISTINCT FROM p_owner OR r.fence<>p_fence OR r.lease_until<=now() THEN RETURN false; END IF;
 IF p_result IN('failed','bounced') AND r.reserved THEN
  UPDATE marketing_quota_windows SET used_count=GREATEST(0,used_count-1) WHERE tenant_id=p_tenant AND id=r.window_id;
 END IF;
 UPDATE marketing_recipients SET status=CASE WHEN p_result='failed' AND r.attempts<5 THEN 'queued' ELSE p_result END,reserved=(p_result NOT IN('failed','bounced')),result_code=p_result,completed_at=CASE WHEN p_result='failed' AND r.attempts<5 THEN NULL ELSE now() END,next_attempt_at=now()+make_interval(secs=>power(2,LEAST(r.attempts,10))::double precision),lease_until=NULL WHERE id=r.id;
 IF p_result='bounced' THEN INSERT INTO marketing_suppressions(tenant_id,email,reason) VALUES(p_tenant,r.email,'bounce') ON CONFLICT(tenant_id,email) DO NOTHING; END IF;
 RETURN true;
END $$;
ALTER FUNCTION urmail_finish_marketing_send(bigint,bigint,text,bigint,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_finish_marketing_send(bigint,bigint,text,bigint,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_finish_marketing_send(bigint,bigint,text,bigint,text) TO urmail_platform;

ALTER TABLE marketing_recipients ADD COLUMN resolution_reason text;
CREATE FUNCTION urmail_resolve_marketing_attempt(p_tenant bigint,p_recipient bigint,p_outcome text,p_reason text) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r marketing_recipients%ROWTYPE; campaign_state text;
BEGIN
 IF current_setting('role',true)<>'urmail_platform' OR p_outcome NOT IN('accepted','not_submitted') OR length(trim(p_reason)) NOT BETWEEN 1 AND 2048 THEN RAISE EXCEPTION 'MARKETING_RESOLUTION_INVALID' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(p_tenant);
 SELECT * INTO r FROM marketing_recipients WHERE tenant_id=p_tenant AND id=p_recipient FOR UPDATE;
 IF NOT FOUND OR r.status<>'uncertain' THEN RETURN false; END IF;
 SELECT status INTO campaign_state FROM marketing_campaigns WHERE tenant_id=p_tenant AND id=r.campaign_id;
 IF p_outcome='not_submitted' AND r.reserved THEN UPDATE marketing_quota_windows SET used_count=GREATEST(0,used_count-1) WHERE tenant_id=p_tenant AND id=r.window_id; END IF;
 UPDATE marketing_recipients SET status=CASE WHEN p_outcome='accepted' THEN 'sent' WHEN campaign_state='canceled' THEN 'canceled' ELSE 'queued' END,reserved=(p_outcome='accepted'),fence=fence+1,lease_owner=NULL,lease_until=NULL,resolution_reason=p_reason,result_code='manually_confirmed_'||p_outcome,next_attempt_at=now() WHERE id=r.id;
 RETURN true;
END $$;
ALTER FUNCTION urmail_resolve_marketing_attempt(bigint,bigint,text,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_resolve_marketing_attempt(bigint,bigint,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_resolve_marketing_attempt(bigint,bigint,text,text) TO urmail_platform;

CREATE FUNCTION urmail_record_marketing_feedback(p_channel bigint,p_event text,p_recipient bigint,p_status text,p_hash text) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r marketing_recipients%ROWTYPE; prior_hash text; tid bigint;
BEGIN
 IF current_setting('role',true)<>'urmail_platform' OR p_status NOT IN('bounced','complaint','deferred') OR length(p_event) NOT BETWEEN 1 AND 200 OR p_hash !~ '^[a-f0-9]{64}$' THEN RAISE EXCEPTION 'MARKETING_FEEDBACK_INVALID' USING ERRCODE='42501'; END IF;
 SELECT tenant_id INTO tid FROM marketing_channels WHERE id=p_channel;
 IF tid IS NULL THEN RETURN 'not_found'; END IF;
 PERFORM pg_advisory_xact_lock(tid);
 SELECT * INTO r FROM marketing_recipients mr WHERE mr.tenant_id=tid AND mr.id=p_recipient AND EXISTS(SELECT 1 FROM marketing_campaigns c WHERE c.tenant_id=tid AND c.id=mr.campaign_id AND c.channel_id=p_channel) FOR UPDATE;
 IF NOT FOUND OR r.status NOT IN('submitting','sent','uncertain','bounced') THEN RETURN 'not_found'; END IF;
 SELECT payload_hash INTO prior_hash FROM marketing_feedback WHERE channel_id=p_channel AND event_id=p_event;
 IF prior_hash IS NOT NULL THEN IF prior_hash=p_hash THEN RETURN 'duplicate'; ELSE RETURN 'conflict'; END IF; END IF;
 INSERT INTO marketing_feedback(channel_id,event_id,tenant_id,recipient_id,status,payload_hash) VALUES(p_channel,p_event,tid,r.id,p_status,p_hash);
 IF p_status IN('bounced','complaint') THEN
  INSERT INTO marketing_suppressions(tenant_id,email,reason) VALUES(tid,r.email,CASE WHEN p_status='bounced' THEN 'bounce' ELSE 'complaint' END)
  ON CONFLICT(tenant_id,email) DO UPDATE SET reason=CASE WHEN marketing_suppressions.reason IN('unsubscribe','bounce','complaint') THEN marketing_suppressions.reason ELSE EXCLUDED.reason END;
  UPDATE marketing_recipients SET status='bounced',result_code=p_status,completed_at=now(),fence=fence+1 WHERE id=r.id;
 END IF;
 RETURN 'recorded';
END $$;
ALTER FUNCTION urmail_record_marketing_feedback(bigint,text,bigint,text,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_record_marketing_feedback(bigint,text,bigint,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_record_marketing_feedback(bigint,text,bigint,text,text) TO urmail_platform;

CREATE FUNCTION urmail_cancel_campaign_tasks() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.status='canceled' THEN
  PERFORM pg_advisory_xact_lock(NEW.tenant_id);
  UPDATE marketing_recipients SET status='canceled',completed_at=now(),result_code='campaign_canceled',fence=fence+1 WHERE tenant_id=NEW.tenant_id AND campaign_id=NEW.id AND status IN('queued','leased');
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_cancel_campaign_tasks() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_cancel_campaign_tasks() FROM PUBLIC;
CREATE TRIGGER marketing_campaign_cancel AFTER UPDATE OF status ON marketing_campaigns FOR EACH ROW WHEN (NEW.status='canceled') EXECUTE FUNCTION urmail_cancel_campaign_tasks();
