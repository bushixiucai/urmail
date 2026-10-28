-- Authoring and immutable published snapshots reuse the existing runtime flows,
-- positions and leases. Entries are the sole fact source for explicit enrollment.
CREATE TABLE marketing_flow_definitions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 draft jsonb NOT NULL CHECK(jsonb_typeof(draft)='object'),
 draft_revision bigint NOT NULL DEFAULT 1 CHECK(draft_revision>0),
 status text NOT NULL DEFAULT 'draft' CHECK(status IN('draft','active','paused')),
 published_version integer NOT NULL DEFAULT 0 CHECK(published_version>=0),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(tenant_id,id)
);
CREATE TABLE marketing_flow_versions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 definition_id bigint NOT NULL, version integer NOT NULL CHECK(version>0),
 draft_revision bigint NOT NULL, runtime_flow_id bigint NOT NULL,
 definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object'),
 published_by bigint NOT NULL REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(tenant_id,id), UNIQUE(tenant_id,definition_id,id),
 UNIQUE(tenant_id,definition_id,version), UNIQUE(tenant_id,definition_id,draft_revision), UNIQUE(tenant_id,runtime_flow_id),
 FOREIGN KEY(tenant_id,definition_id) REFERENCES marketing_flow_definitions(tenant_id,id),
 FOREIGN KEY(tenant_id,runtime_flow_id) REFERENCES marketing_flows(tenant_id,id)
);
CREATE TABLE marketing_flow_content_snapshots (
 tenant_id bigint NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, runtime_flow_id bigint NOT NULL, step_key text NOT NULL,
 template_id bigint NOT NULL, subject text NOT NULL, text_body text NOT NULL,
 PRIMARY KEY(tenant_id,runtime_flow_id,step_key),
 FOREIGN KEY(tenant_id,runtime_flow_id) REFERENCES marketing_flows(tenant_id,id)
);
CREATE TABLE marketing_flow_entries (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, definition_id bigint NOT NULL, version_id bigint NOT NULL,
 event_key text NOT NULL CHECK(length(event_key) BETWEEN 8 AND 128),
 source text NOT NULL CHECK(source='contact_enrolled'), list_id bigint NOT NULL,
 recipient_count integer NOT NULL CHECK(recipient_count BETWEEN 0 AND 1000),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(tenant_id,id), UNIQUE(tenant_id,definition_id,event_key),
 FOREIGN KEY(tenant_id,definition_id,version_id) REFERENCES marketing_flow_versions(tenant_id,definition_id,id),
 FOREIGN KEY(tenant_id,list_id) REFERENCES marketing_lists(tenant_id,id)
);
ALTER TABLE marketing_flow_steps DROP CONSTRAINT marketing_flow_steps_kind_check;
ALTER TABLE marketing_flow_steps ADD CONSTRAINT marketing_flow_steps_kind_check CHECK(kind IN('send','wait','condition','stop'));
ALTER TABLE marketing_flow_steps
 ADD COLUMN condition_field text NOT NULL DEFAULT '',
 ADD COLUMN condition_operator text NOT NULL DEFAULT '',
 ADD COLUMN condition_value text NOT NULL DEFAULT '';
ALTER TABLE marketing_flow_positions
 ADD COLUMN entry_id bigint,
 ADD COLUMN event_facts jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(event_facts)='object'),
 ADD COLUMN last_error text NOT NULL DEFAULT '' CHECK(length(last_error)<=100),
 ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now(),
 ADD COLUMN attempt_count integer NOT NULL DEFAULT 0 CHECK(attempt_count>=0),
 ADD COLUMN stopped_at timestamptz,
 ADD COLUMN flow_window_id bigint,
 ADD COLUMN flow_reserved boolean NOT NULL DEFAULT false,
 ADD FOREIGN KEY(tenant_id,flow_window_id) REFERENCES marketing_quota_windows(tenant_id,id),
 ADD CONSTRAINT marketing_flow_positions_tenant_id_id_key UNIQUE(tenant_id,id),
 ADD FOREIGN KEY(tenant_id,entry_id) REFERENCES marketing_flow_entries(tenant_id,id);
CREATE TABLE marketing_flow_node_executions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, position_id bigint NOT NULL, lease_fence bigint NOT NULL,
 node_key text NOT NULL, status text NOT NULL CHECK(status IN('submitted','advanced','waiting','blocked','failed','uncertain')),
 error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=100), created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(tenant_id,position_id,lease_fence),
 FOREIGN KEY(tenant_id,position_id) REFERENCES marketing_flow_positions(tenant_id,id)
);
DO $$ DECLARE n text; BEGIN
 FOREACH n IN ARRAY ARRAY['marketing_flow_definitions','marketing_flow_versions','marketing_flow_content_snapshots','marketing_flow_entries','marketing_flow_node_executions'] LOOP
  EXECUTE format('ALTER TABLE %I OWNER TO urmail_platform',n);
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',n);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',n);
  EXECUTE format('CREATE POLICY tenant_isolation ON %I USING(tenant_id=NULLIF(current_setting(''urmail.tenant_id'',true),'''')::bigint) WITH CHECK(tenant_id=NULLIF(current_setting(''urmail.tenant_id'',true),'''')::bigint)',n);
  EXECUTE format('REVOKE ALL ON %I FROM PUBLIC',n);
  EXECUTE format('GRANT SELECT ON %I TO urmail_app,urmail_platform',n);
 END LOOP;
END $$;
GRANT INSERT,UPDATE ON marketing_flow_definitions TO urmail_app,urmail_platform;
GRANT INSERT ON marketing_flow_versions,marketing_flow_content_snapshots,marketing_flow_entries TO urmail_app,urmail_platform;
GRANT INSERT ON marketing_flow_node_executions TO urmail_platform;

CREATE FUNCTION urmail_guard_published_marketing_step() RETURNS trigger LANGUAGE plpgsql
SET search_path=pg_catalog,public AS $$ BEGIN
 IF TG_OP='DELETE' AND NOT EXISTS(SELECT 1 FROM tenants WHERE id=OLD.tenant_id) THEN RETURN OLD; END IF;
 IF EXISTS(SELECT 1 FROM marketing_flow_versions WHERE tenant_id=COALESCE(NEW.tenant_id,OLD.tenant_id) AND runtime_flow_id=COALESCE(NEW.flow_id,OLD.flow_id)) THEN
  RAISE EXCEPTION 'PUBLISHED_FLOW_IMMUTABLE' USING ERRCODE='23514';
 END IF;
 RETURN COALESCE(NEW,OLD);
END $$;
ALTER FUNCTION urmail_guard_published_marketing_step() OWNER TO urmail_platform;
CREATE TRIGGER guard_published_marketing_step BEFORE INSERT OR UPDATE OR DELETE ON marketing_flow_steps FOR EACH ROW EXECUTE FUNCTION urmail_guard_published_marketing_step();

-- The lease is checked under the same row lock as advancement. SMTP DATA is
-- durably marked uncertain before transmission; only a confirmed result from
-- that exact live lease may clear it. Crashes never auto-replay new versions.
CREATE FUNCTION urmail_commit_marketing_flow_fenced(p_tenant bigint,p_flow bigint,p_email text,p_owner text,p_fence bigint,p_next text,p_unsubscribed boolean,p_confirmed boolean)
RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE current marketing_flow_positions%ROWTYPE; BEGIN
 SELECT * INTO current FROM marketing_flow_positions WHERE tenant_id=p_tenant AND flow_id=p_flow AND email=p_email FOR UPDATE;
 IF NOT FOUND THEN RETURN 'missing'; END IF;
 IF p_owner IS NULL OR p_owner='' OR current.lease_owner IS DISTINCT FROM p_owner OR current.lease_fence<>p_fence OR current.lease_until IS NULL OR current.lease_until<=now() THEN RETURN 'stale_lease'; END IF;
 IF p_confirmed THEN UPDATE marketing_flow_positions SET delivery_uncertain=false,flow_reserved=false,flow_window_id=NULL WHERE tenant_id=p_tenant AND flow_id=p_flow AND email=p_email; END IF;
 IF current.stopped_at IS NOT NULL OR NOT EXISTS(SELECT 1 FROM marketing_flows WHERE tenant_id=p_tenant AND id=p_flow AND status='active') THEN RETURN 'blocked'; END IF;
 IF p_next<>'' AND NOT EXISTS(SELECT 1 FROM marketing_flow_steps WHERE tenant_id=p_tenant AND flow_id=p_flow AND step_key=p_next) THEN RETURN 'blocked'; END IF;
 IF current.delivery_uncertain AND NOT p_confirmed THEN RETURN 'uncertain'; END IF;
 RETURN urmail_commit_marketing_flow_step(p_tenant,p_flow,p_email,CASE WHEN p_unsubscribed THEN 'unsubscribed' ELSE '' END,p_next);
END $$;
ALTER FUNCTION urmail_commit_marketing_flow_fenced(bigint,bigint,text,text,bigint,text,boolean,boolean) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_commit_marketing_flow_fenced(bigint,bigint,text,text,bigint,text,boolean,boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_commit_marketing_flow_fenced(bigint,bigint,text,text,bigint,text,boolean,boolean) TO urmail_platform;

-- Campaigns and versioned flows reserve from the same tenant calendar window.
CREATE FUNCTION urmail_begin_marketing_flow_send(p_tenant bigint,p_flow bigint,p_email text,p_owner text,p_fence bigint,p_channel bigint)
RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r marketing_flow_positions%ROWTYPE; plan plans%ROWTYPE; w marketing_quota_windows%ROWTYPE;
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
 SELECT * INTO plan FROM urmail_effective_plan(p_tenant);
 IF COALESCE(plan.features->>'marketing_daily_recipient_limit','') !~ '^[1-9][0-9]{0,8}$' OR COALESCE(plan.features->>'marketing_rate_per_minute','') !~ '^[1-9][0-9]{0,5}$' THEN RETURN 'quota_unconfigured'; END IF;
 day_limit:=(plan.features->>'marketing_daily_recipient_limit')::bigint;rate_limit:=(plan.features->>'marketing_rate_per_minute')::bigint;
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

CREATE FUNCTION urmail_release_marketing_flow_reservation(p_tenant bigint,p_flow bigint,p_email text,p_owner text,p_fence bigint)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r marketing_flow_positions%ROWTYPE; BEGIN
 PERFORM pg_advisory_xact_lock(p_tenant);
 SELECT * INTO r FROM marketing_flow_positions WHERE tenant_id=p_tenant AND flow_id=p_flow AND email=p_email FOR UPDATE;
 IF NOT FOUND OR r.lease_owner IS DISTINCT FROM p_owner OR r.lease_fence<>p_fence OR r.lease_until IS NULL OR r.lease_until<=now() THEN RETURN false; END IF;
 IF r.flow_reserved THEN UPDATE marketing_quota_windows SET used_count=GREATEST(0,used_count-1) WHERE tenant_id=p_tenant AND id=r.flow_window_id; END IF;
 UPDATE marketing_flow_positions SET delivery_uncertain=false,flow_reserved=false,flow_window_id=NULL WHERE tenant_id=p_tenant AND id=r.id;
 RETURN true;
END $$;
ALTER FUNCTION urmail_release_marketing_flow_reservation(bigint,bigint,text,text,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_release_marketing_flow_reservation(bigint,bigint,text,text,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_release_marketing_flow_reservation(bigint,bigint,text,text,bigint) TO urmail_platform;

CREATE FUNCTION urmail_retry_marketing_flow_instance(p_tenant bigint,p_definition bigint,p_position bigint,p_confirmed_not_accepted boolean)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r marketing_flow_positions%ROWTYPE; BEGIN
 IF current_setting('role',true)<>'urmail_platform' AND (p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=p_tenant AND user_id=nullif(current_setting('urmail.user_id',true),'')::bigint AND role IN('owner','admin'))) THEN RAISE EXCEPTION 'MARKETING_ADMIN_REQUIRED' USING ERRCODE='42501'; END IF;
 PERFORM pg_advisory_xact_lock(p_tenant);
 SELECT p.* INTO r FROM marketing_flow_positions p JOIN marketing_flow_versions v ON v.tenant_id=p.tenant_id AND v.runtime_flow_id=p.flow_id
 JOIN marketing_flows f ON f.tenant_id=p.tenant_id AND f.id=p.flow_id
 WHERE p.tenant_id=p_tenant AND p.id=p_position AND v.definition_id=p_definition AND f.status='active' FOR UPDATE OF p;
 IF NOT FOUND OR r.stopped_at IS NOT NULL OR r.unsubscribed OR r.lease_until>now() OR (NOT r.delivery_uncertain AND r.last_error='') OR (r.delivery_uncertain AND NOT p_confirmed_not_accepted) THEN RETURN false; END IF;
 IF r.flow_reserved THEN UPDATE marketing_quota_windows SET used_count=GREATEST(0,used_count-1) WHERE tenant_id=p_tenant AND id=r.flow_window_id; END IF;
 UPDATE marketing_flow_positions SET delivery_uncertain=false,flow_reserved=false,flow_window_id=NULL,last_error='',next_attempt_at=now(),lease_owner=NULL,lease_until=NULL,lease_fence=lease_fence+1,updated_at=now() WHERE tenant_id=p_tenant AND id=r.id;
 RETURN true;
END $$;
ALTER FUNCTION urmail_retry_marketing_flow_instance(bigint,bigint,bigint,boolean) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_retry_marketing_flow_instance(bigint,bigint,bigint,boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_retry_marketing_flow_instance(bigint,bigint,bigint,boolean) TO urmail_app,urmail_platform;

CREATE FUNCTION urmail_guard_marketing_snapshot() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$ BEGIN
 IF TG_OP='DELETE' AND NOT EXISTS(SELECT 1 FROM tenants WHERE id=OLD.tenant_id) THEN RETURN OLD; END IF;
 RAISE EXCEPTION 'PUBLISHED_FLOW_IMMUTABLE' USING ERRCODE='23514';
END $$;
ALTER FUNCTION urmail_guard_marketing_snapshot() OWNER TO urmail_platform;
CREATE TRIGGER guard_marketing_version BEFORE UPDATE OR DELETE ON marketing_flow_versions FOR EACH ROW EXECUTE FUNCTION urmail_guard_marketing_snapshot();
CREATE TRIGGER guard_marketing_content BEFORE UPDATE OR DELETE ON marketing_flow_content_snapshots FOR EACH ROW EXECUTE FUNCTION urmail_guard_marketing_snapshot();

CREATE FUNCTION urmail_record_marketing_flow_audit(p_tenant bigint,p_definition bigint,p_action text,p_detail jsonb)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE actor bigint:=nullif(current_setting('urmail.user_id',true),'')::bigint; BEGIN
 IF p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint
 OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=p_tenant AND user_id=actor AND role IN('owner','admin'))
 OR NOT EXISTS(SELECT 1 FROM marketing_flow_definitions WHERE tenant_id=p_tenant AND id=p_definition) THEN RAISE EXCEPTION 'MARKETING_ADMIN_REQUIRED' USING ERRCODE='42501'; END IF;
 IF p_action NOT IN('published','deactivated','enrolled','retry_requested','instance_stopped') OR jsonb_typeof(p_detail)<>'object' OR octet_length(p_detail::text)>4000 THEN RAISE EXCEPTION 'INVALID_FLOW_AUDIT' USING ERRCODE='22023'; END IF;
 INSERT INTO audit_logs(tenant_id,actor_user_id,action,target_type,target_id,detail) VALUES(p_tenant,actor,'marketing.flow.'||p_action,'marketing_flow',p_definition::text,p_detail);
END $$;
ALTER FUNCTION urmail_record_marketing_flow_audit(bigint,bigint,text,jsonb) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_record_marketing_flow_audit(bigint,bigint,text,jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_record_marketing_flow_audit(bigint,bigint,text,jsonb) TO urmail_app;

-- Suppression is authoritative for every existing position, including waits.
CREATE FUNCTION urmail_stop_suppressed_marketing_flows() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$ BEGIN
 UPDATE marketing_flow_positions p SET unsubscribed=true,updated_at=now() WHERE p.tenant_id=NEW.tenant_id AND p.email=NEW.email AND NOT p.unsubscribed AND p.stopped_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM marketing_flow_steps s WHERE s.tenant_id=p.tenant_id AND s.flow_id=p.flow_id AND s.step_key=p.step_key AND s.kind='stop');
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_stop_suppressed_marketing_flows() OWNER TO urmail_platform;
CREATE TRIGGER stop_suppressed_marketing_flows AFTER INSERT OR UPDATE ON marketing_suppressions FOR EACH ROW EXECUTE FUNCTION urmail_stop_suppressed_marketing_flows();

CREATE FUNCTION urmail_stop_revoked_flow_contacts() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$ BEGIN
 UPDATE marketing_flow_positions p SET unsubscribed=true,updated_at=now() WHERE p.tenant_id=NEW.tenant_id AND p.email=NEW.email AND NOT p.unsubscribed AND p.stopped_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM marketing_flow_steps s WHERE s.tenant_id=p.tenant_id AND s.flow_id=p.flow_id AND s.step_key=p.step_key AND s.kind='stop')
 AND EXISTS(SELECT 1 FROM marketing_flow_entries e WHERE e.tenant_id=p.tenant_id AND e.id=p.entry_id AND e.list_id=NEW.list_id);
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_stop_revoked_flow_contacts() OWNER TO urmail_platform;
CREATE TRIGGER stop_revoked_flow_contacts AFTER UPDATE OF opted_in ON marketing_contacts FOR EACH ROW WHEN(OLD.opted_in AND NOT NEW.opted_in) EXECUTE FUNCTION urmail_stop_revoked_flow_contacts();
