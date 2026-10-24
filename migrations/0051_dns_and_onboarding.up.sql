-- Existing domains keep their historical readiness. API-created domains must
-- pass the durable onboarding workflow before transport or mailbox access.
ALTER TABLE domains ADD COLUMN provisioning_state text NOT NULL DEFAULT 'ready' CHECK(provisioning_state IN('pending','ready','blocked'));
ALTER TABLE domains ADD COLUMN dns_explicit boolean NOT NULL DEFAULT false;
ALTER TABLE domains ADD COLUMN custom_web_enabled boolean NOT NULL DEFAULT true;
REVOKE UPDATE ON domains FROM urmail_app;
GRANT UPDATE(fqdn,mode,status,verification_token,verified_at,dkim_selector,dkim_private_key,updated_at,verification_issued_at,verification_expires_at) ON domains TO urmail_app;
ALTER TABLE onboarding_tasks ADD COLUMN fence bigint NOT NULL DEFAULT 0,
 ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now(),
 ADD COLUMN desired_generation text,
 ADD COLUMN last_error_code text;
ALTER TABLE onboarding_tasks DROP CONSTRAINT onboarding_tasks_step_check;
ALTER TABLE onboarding_tasks ADD CONSTRAINT onboarding_tasks_step_check CHECK(step IN('domain_verification','bootstrap_config','dns_propagation','certificate_issue','mailbox_provision','configuration_apply','protocol_readiness','ready'));
-- Expired work is retried only after a new fenced claim. A restored snapshot
-- is separately blocked by the restore maintenance classifier.
CREATE INDEX onboarding_due_idx ON onboarding_tasks(next_attempt_at,id) WHERE status IN('pending','running');

CREATE TABLE dns_credentials(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id),domain_id bigint NOT NULL,
 provider text NOT NULL CHECK(provider IN('cloudflare','aliyun','dnspod','tencent')),
 zone_name text NOT NULL,zone_id text NOT NULL,
 encrypted_secret text NOT NULL CHECK(encrypted_secret LIKE 'enc:v1:%'),
 version bigint NOT NULL DEFAULT 1,
 enabled boolean NOT NULL DEFAULT true,
 verified_at timestamptz NOT NULL DEFAULT now(),revoked_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,domain_id) REFERENCES domains(tenant_id,id),
 UNIQUE(tenant_id,id),UNIQUE(tenant_id,domain_id)
);
CREATE TABLE dns_change_plans(
 id uuid PRIMARY KEY,tenant_id bigint NOT NULL REFERENCES tenants(id),domain_id bigint NOT NULL,
 credential_id bigint,credential_version bigint,
 kind text NOT NULL CHECK(kind IN('provider','managed','manual')),
 status text NOT NULL DEFAULT 'draft' CHECK(status IN('draft','confirmed','applying','completed','uncertain','canceled','failed','rollback_pending','rolled_back')),
 domain_digest text NOT NULL CHECK(length(domain_digest)=64),
 source_digest text NOT NULL CHECK(length(source_digest)=64),
 before_records jsonb NOT NULL,desired_records jsonb NOT NULL,changes jsonb NOT NULL,
 created_by bigint NOT NULL REFERENCES users(id),confirmed_by bigint REFERENCES users(id),
 confirmed_at timestamptz,expires_at timestamptz NOT NULL,
 fence bigint NOT NULL DEFAULT 0,lease_owner text,lease_until timestamptz,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),last_error_code text,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,domain_id) REFERENCES domains(tenant_id,id),
 FOREIGN KEY(tenant_id,credential_id) REFERENCES dns_credentials(tenant_id,id),UNIQUE(tenant_id,id)
);
CREATE UNIQUE INDEX dns_domain_active_plan ON dns_change_plans(domain_id) WHERE status IN('confirmed','applying','uncertain','rollback_pending');
CREATE TABLE dns_record_actions(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,tenant_id bigint NOT NULL,
 plan_id uuid NOT NULL,ordinal integer NOT NULL,
 kind text NOT NULL CHECK(kind IN('create','update')),
 before_record jsonb,after_record jsonb NOT NULL,
 provider_record_id text,observed_record jsonb,owned boolean NOT NULL DEFAULT false,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN('pending','submitted','applied','uncertain','rolled_back')),
 last_error_code text,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,plan_id) REFERENCES dns_change_plans(tenant_id,id),UNIQUE(plan_id,ordinal)
);
CREATE TABLE managed_dns_zones(
 tenant_id bigint NOT NULL,domain_id bigint PRIMARY KEY,
 records jsonb NOT NULL,plan_id uuid NOT NULL,updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,domain_id) REFERENCES domains(tenant_id,id),
 FOREIGN KEY(tenant_id,plan_id) REFERENCES dns_change_plans(tenant_id,id)
);
DO $$ DECLARE n text;BEGIN
 FOREACH n IN ARRAY ARRAY['dns_credentials','dns_change_plans','dns_record_actions','managed_dns_zones'] LOOP
 EXECUTE format('ALTER TABLE %I OWNER TO urmail_platform',n);
 EXECUTE format('REVOKE ALL ON %I FROM PUBLIC,urmail_app,urmail_dovecot,urmail_roundcube',n);
 EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',n);
 EXECUTE format('CREATE POLICY tenant_isolation ON %I USING(tenant_id=nullif(current_setting(''urmail.tenant_id'',true),'''')::bigint)',n);
 END LOOP;
END $$;
REVOKE ALL ON SEQUENCE dns_credentials_id_seq,dns_record_actions_id_seq FROM PUBLIC,urmail_app,urmail_dovecot,urmail_roundcube;
GRANT SELECT(id,tenant_id,domain_id,provider,zone_name,zone_id,version,enabled,verified_at,revoked_at,created_at,updated_at) ON dns_credentials TO urmail_app;
GRANT SELECT ON dns_change_plans,dns_record_actions,managed_dns_zones TO urmail_app;
CREATE FUNCTION urmail_dns_actor(p_tenant bigint,p_domain bigint) RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE uid bigint:=nullif(current_setting('urmail.user_id',true),'')::bigint;
BEGIN
 IF p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=p_tenant AND user_id=uid AND role IN('owner','admin')) OR NOT EXISTS(SELECT 1 FROM domains WHERE tenant_id=p_tenant AND id=p_domain AND status IN('active','disabled') AND verified_at IS NOT NULL AND verified_at<=now()) THEN RAISE EXCEPTION 'DNS_AUTHORIZATION_REQUIRED' USING ERRCODE='42501';END IF;
 RETURN uid;
END $$;
ALTER FUNCTION urmail_dns_actor(bigint,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_dns_actor(bigint,bigint) FROM PUBLIC;
CREATE FUNCTION urmail_save_dns_credential(p_tenant bigint,p_domain bigint,p_provider text,p_zone text,p_zone_id text,p_secret text) RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE cid bigint;BEGIN
 PERFORM urmail_dns_actor(p_tenant,p_domain);
 IF NOT EXISTS(SELECT 1 FROM domains WHERE tenant_id=p_tenant AND id=p_domain AND status='active') THEN RAISE EXCEPTION 'DNS_DOMAIN_DISABLED' USING ERRCODE='23514';END IF;
 IF p_zone='' OR p_zone_id='' OR p_secret NOT LIKE 'enc:v1:%' OR NOT EXISTS(SELECT 1 FROM domains WHERE tenant_id=p_tenant AND id=p_domain AND (fqdn=p_zone OR right(fqdn,length(p_zone)+1)='.'||p_zone)) THEN RAISE EXCEPTION 'DNS_ZONE_MISMATCH' USING ERRCODE='23514';END IF;
 PERFORM pg_advisory_xact_lock(51,hashtext(p_domain::text));
 INSERT INTO dns_credentials(tenant_id,domain_id,provider,zone_name,zone_id,encrypted_secret) VALUES(p_tenant,p_domain,p_provider,p_zone,p_zone_id,p_secret) ON CONFLICT(tenant_id,domain_id) DO UPDATE SET provider=EXCLUDED.provider,zone_name=EXCLUDED.zone_name,zone_id=EXCLUDED.zone_id,encrypted_secret=EXCLUDED.encrypted_secret,version=dns_credentials.version+1,enabled=true,revoked_at=NULL,verified_at=now(),updated_at=now() RETURNING id INTO cid;
 UPDATE dns_change_plans SET status=CASE WHEN status='draft' THEN 'canceled' ELSE 'uncertain' END,fence=fence+1,lease_owner=NULL,lease_until=NULL,last_error_code='DNS_CREDENTIAL_CHANGED' WHERE tenant_id=p_tenant AND domain_id=p_domain AND status IN('draft','confirmed','applying','rollback_pending');
 RETURN cid;
END $$;
ALTER FUNCTION urmail_save_dns_credential(bigint,bigint,text,text,text,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_save_dns_credential(bigint,bigint,text,text,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_save_dns_credential(bigint,bigint,text,text,text,text) TO urmail_app;
CREATE FUNCTION urmail_revoke_dns_credential(p_tenant bigint,p_domain bigint) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 PERFORM urmail_dns_actor(p_tenant,p_domain);PERFORM pg_advisory_xact_lock(51,hashtext(p_domain::text));
 UPDATE dns_credentials SET enabled=false,version=version+1,revoked_at=now(),updated_at=now() WHERE tenant_id=p_tenant AND domain_id=p_domain;
 UPDATE dns_change_plans SET status=CASE WHEN status='draft' THEN 'canceled' ELSE 'uncertain' END,fence=fence+1,lease_owner=NULL,lease_until=NULL,last_error_code='DNS_CREDENTIAL_REVOKED' WHERE tenant_id=p_tenant AND domain_id=p_domain AND status IN('draft','confirmed','applying','rollback_pending');
END $$;
ALTER FUNCTION urmail_revoke_dns_credential(bigint,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_revoke_dns_credential(bigint,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_revoke_dns_credential(bigint,bigint) TO urmail_app;
CREATE FUNCTION urmail_domain_mail_ready(p_tenant bigint,p_domain bigint) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$ SELECT EXISTS(SELECT 1 FROM domains WHERE tenant_id=p_tenant AND id=p_domain AND status='active' AND verified_at IS NOT NULL AND provisioning_state='ready'); $$;
ALTER FUNCTION urmail_domain_mail_ready(bigint,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_domain_mail_ready(bigint,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_domain_mail_ready(bigint,bigint) TO urmail_app,urmail_dovecot;
DO $$ DECLARE definition text;BEGIN
 SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure) INTO definition;
 definition:=replace(definition,'t.status=''active''','t.status=''active'' AND /* onboarding_domain_gate */ (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=t.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=t.id AND gd.status=''active'' AND gd.provisioning_state=''ready''))');
 IF strpos(definition,'onboarding_domain_gate')=0 THEN RAISE EXCEPTION 'MAIL_ONBOARDING_GATE_UPDATE_REQUIRED';END IF;
 EXECUTE definition;
END $$;

CREATE FUNCTION urmail_domain_provisioning_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$ BEGIN
 IF current_setting('role',true) NOT IN('urmail_platform','urmail_migrate') THEN
  IF TG_OP='INSERT' THEN NEW.provisioning_state:='pending';NEW.dns_explicit:=false;NEW.custom_web_enabled:=false;
  ELSIF (NEW.provisioning_state IS DISTINCT FROM OLD.provisioning_state AND NEW.provisioning_state<>'pending') OR NEW.dns_explicit IS DISTINCT FROM OLD.dns_explicit THEN RAISE EXCEPTION 'DOMAIN_PROVISIONING_FORBIDDEN' USING ERRCODE='42501';END IF;
 END IF;
 IF TG_OP='UPDATE' AND (NEW.mode IS DISTINCT FROM OLD.mode OR NEW.dkim_selector IS DISTINCT FROM OLD.dkim_selector OR NEW.dkim_private_key IS DISTINCT FROM OLD.dkim_private_key) THEN NEW.provisioning_state:='pending';NEW.dns_explicit:=false;END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_domain_provisioning_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_domain_provisioning_guard() FROM PUBLIC;
CREATE TRIGGER domain_provisioning_guard BEFORE INSERT OR UPDATE ON domains FOR EACH ROW EXECUTE FUNCTION urmail_domain_provisioning_guard();
CREATE FUNCTION urmail_create_dns_plan(p_id uuid,p_tenant bigint,p_domain bigint,p_credential bigint,p_version bigint,p_kind text,p_domain_digest text,p_source_digest text,p_before jsonb,p_desired jsonb,p_changes jsonb) RETURNS uuid LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE uid bigint;BEGIN
 uid:=urmail_dns_actor(p_tenant,p_domain);IF NOT EXISTS(SELECT 1 FROM domains WHERE tenant_id=p_tenant AND id=p_domain AND status='active') THEN RAISE EXCEPTION 'DNS_DOMAIN_DISABLED' USING ERRCODE='23514';END IF;PERFORM pg_advisory_xact_lock(51,hashtext(p_domain::text));
 IF p_kind='provider' AND NOT EXISTS(SELECT 1 FROM dns_credentials WHERE tenant_id=p_tenant AND domain_id=p_domain AND id=p_credential AND version=p_version AND enabled AND revoked_at IS NULL) THEN RAISE EXCEPTION 'DNS_CREDENTIAL_CHANGED' USING ERRCODE='23514';END IF;
 IF p_kind<>'provider' AND p_credential IS NOT NULL THEN RAISE EXCEPTION 'DNS_PLAN_INVALID' USING ERRCODE='23514';END IF;
 INSERT INTO dns_change_plans(id,tenant_id,domain_id,credential_id,credential_version,kind,domain_digest,source_digest,before_records,desired_records,changes,created_by,expires_at) VALUES(p_id,p_tenant,p_domain,p_credential,p_version,p_kind,p_domain_digest,p_source_digest,p_before,p_desired,p_changes,uid,now()+interval '15 minutes');RETURN p_id;
END $$;
ALTER FUNCTION urmail_create_dns_plan(uuid,bigint,bigint,bigint,bigint,text,text,text,jsonb,jsonb,jsonb) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_create_dns_plan(uuid,bigint,bigint,bigint,bigint,text,text,text,jsonb,jsonb,jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_create_dns_plan(uuid,bigint,bigint,bigint,bigint,text,text,text,jsonb,jsonb,jsonb) TO urmail_app;
CREATE FUNCTION urmail_confirm_dns_plan(p_tenant bigint,p_id uuid,p_digest text) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE p dns_change_plans%ROWTYPE;uid bigint;BEGIN
 SELECT * INTO p FROM dns_change_plans WHERE tenant_id=p_tenant AND id=p_id FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'DNS_PLAN_NOT_FOUND' USING ERRCODE='23514';END IF;
 uid:=urmail_dns_actor(p_tenant,p.domain_id);IF NOT EXISTS(SELECT 1 FROM domains WHERE tenant_id=p_tenant AND id=p.domain_id AND status='active') THEN RAISE EXCEPTION 'DNS_DOMAIN_DISABLED' USING ERRCODE='23514';END IF;PERFORM pg_advisory_xact_lock(51,hashtext(p.domain_id::text));
 IF p.status<>'draft' OR p.expires_at<=now() OR p.source_digest<>p_digest OR jsonb_array_length(p.changes->'conflicts')>0 THEN RAISE EXCEPTION 'DNS_PLAN_CONFIRMATION_INVALID' USING ERRCODE='23514';END IF;
 IF p.kind='provider' AND NOT EXISTS(SELECT 1 FROM dns_credentials WHERE tenant_id=p_tenant AND id=p.credential_id AND version=p.credential_version AND enabled AND revoked_at IS NULL) THEN RAISE EXCEPTION 'DNS_CREDENTIAL_CHANGED' USING ERRCODE='23514';END IF;
 UPDATE dns_change_plans SET status='confirmed',confirmed_by=uid,confirmed_at=now(),updated_at=now() WHERE tenant_id=p_tenant AND id=p_id;
END $$;
ALTER FUNCTION urmail_confirm_dns_plan(bigint,uuid,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_confirm_dns_plan(bigint,uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_confirm_dns_plan(bigint,uuid,text) TO urmail_app;
CREATE FUNCTION urmail_cancel_dns_plan(p_tenant bigint,p_id uuid,p_reason text) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE p dns_change_plans%ROWTYPE;BEGIN
 SELECT * INTO p FROM dns_change_plans WHERE tenant_id=p_tenant AND id=p_id FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'DNS_PLAN_NOT_FOUND' USING ERRCODE='23514';END IF;
 PERFORM urmail_dns_actor(p_tenant,p.domain_id);
 IF p.status NOT IN('draft','confirmed') OR length(trim(p_reason))<10 THEN RAISE EXCEPTION 'DNS_PLAN_CANCEL_INVALID' USING ERRCODE='23514';END IF;
 UPDATE dns_change_plans SET status='canceled',fence=fence+1,lease_owner=NULL,lease_until=NULL,last_error_code='CANCELED_BY_TENANT',updated_at=now() WHERE tenant_id=p_tenant AND id=p_id;
END $$;
ALTER FUNCTION urmail_cancel_dns_plan(bigint,uuid,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_cancel_dns_plan(bigint,uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_cancel_dns_plan(bigint,uuid,text) TO urmail_app;
CREATE FUNCTION urmail_request_onboarding(p_tenant bigint,p_domain bigint,p_reason text) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 PERFORM urmail_dns_actor(p_tenant,p_domain);
 IF NOT EXISTS(SELECT 1 FROM domains WHERE tenant_id=p_tenant AND id=p_domain AND status='active') OR length(trim(p_reason))<10 OR NOT EXISTS(SELECT 1 FROM subscriptions WHERE tenant_id=p_tenant AND status IN('active','trialing','past_due') AND period_start<=now() AND period_end>now()) THEN RAISE EXCEPTION 'ONBOARDING_RETRY_INVALID' USING ERRCODE='23514';END IF;
 -- Restored work requires a fresh DNS snapshot/explicit revalidation first.
 IF EXISTS(SELECT 1 FROM onboarding_tasks WHERE tenant_id=p_tenant AND domain_id=p_domain AND status='blocked' AND (coalesce(last_error_code,'')||coalesce(last_error,'')) LIKE '%RESTORE_RECONCILIATION_REQUIRED%') AND NOT EXISTS(SELECT 1 FROM dns_change_plans WHERE tenant_id=p_tenant AND domain_id=p_domain AND status='completed' AND updated_at>(SELECT max(updated_at) FROM onboarding_tasks WHERE tenant_id=p_tenant AND domain_id=p_domain AND status='blocked')) THEN RAISE EXCEPTION 'RESTORE_DNS_RECONCILIATION_REQUIRED' USING ERRCODE='23514';END IF;
 UPDATE onboarding_tasks SET status='failed',fence=fence+1,leased_until=NULL,leased_by=NULL,last_error_code='SUPERSEDED_BY_EXPLICIT_RETRY',updated_at=now() WHERE tenant_id=p_tenant AND domain_id=p_domain AND status IN('pending','running','failed','blocked');
 INSERT INTO onboarding_tasks(tenant_id,domain_id,status,step,max_attempts) VALUES(p_tenant,p_domain,'pending','bootstrap_config',12);
 UPDATE domains SET provisioning_state='pending' WHERE tenant_id=p_tenant AND id=p_domain;
END $$;
ALTER FUNCTION urmail_request_onboarding(bigint,bigint,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_request_onboarding(bigint,bigint,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_request_onboarding(bigint,bigint,text) TO urmail_app;
DO $$ DECLARE definition text;BEGIN
 SELECT pg_get_functiondef('urmail_take_mailbox_quota(bigint,bigint)'::regprocedure) INTO definition;
 definition:=replace(definition,'AND d.status=''active''','AND d.status=''active'' AND /* onboarding_mailbox_gate */ d.provisioning_state=''ready''');
 IF strpos(definition,'onboarding_mailbox_gate')=0 THEN RAISE EXCEPTION 'MAILBOX_ONBOARDING_GATE_UPDATE_REQUIRED';END IF;EXECUTE definition;
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'ELSE
  IF TG_OP=''INSERT''','ELSE
  IF NOT urmail_domain_mail_ready(tid,NEW.domain_id) THEN RAISE EXCEPTION ''DOMAIN_NOT_PROVISIONED'' USING ERRCODE=''23514'';END IF;
  IF TG_OP=''INSERT''');
 IF strpos(definition,'DOMAIN_NOT_PROVISIONED')=0 THEN RAISE EXCEPTION 'RESOURCE_ONBOARDING_GATE_UPDATE_REQUIRED';END IF;EXECUTE definition;
END $$;
CREATE FUNCTION urmail_rollback_dns_plan(p_tenant bigint,p_id uuid,p_reason text) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE p dns_change_plans%ROWTYPE;BEGIN
 SELECT * INTO p FROM dns_change_plans WHERE tenant_id=p_tenant AND id=p_id FOR UPDATE;IF NOT FOUND THEN RAISE EXCEPTION 'DNS_PLAN_NOT_FOUND' USING ERRCODE='23514';END IF;
 PERFORM urmail_dns_actor(p_tenant,p.domain_id);
 IF p.status<>'completed' OR length(trim(p_reason))<10 OR p.kind='manual' THEN RAISE EXCEPTION 'DNS_ROLLBACK_REQUIRES_REVIEW' USING ERRCODE='23514';END IF;
 UPDATE domains SET provisioning_state='pending' WHERE tenant_id=p_tenant AND id=p.domain_id;
 UPDATE dns_change_plans SET status='rollback_pending',fence=fence+1,lease_owner=NULL,lease_until=NULL,last_error_code='ROLLBACK_EXPLICITLY_REQUESTED',updated_at=now() WHERE tenant_id=p_tenant AND id=p_id;
END $$;
ALTER FUNCTION urmail_rollback_dns_plan(bigint,uuid,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_rollback_dns_plan(bigint,uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_rollback_dns_plan(bigint,uuid,text) TO urmail_app;
CREATE FUNCTION urmail_review_dns_plan(p_tenant bigint,p_id uuid,p_reason text) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE p dns_change_plans%ROWTYPE;BEGIN
 SELECT * INTO p FROM dns_change_plans WHERE tenant_id=p_tenant AND id=p_id FOR UPDATE;IF NOT FOUND THEN RAISE EXCEPTION 'DNS_PLAN_NOT_FOUND' USING ERRCODE='23514';END IF;
 PERFORM urmail_dns_actor(p_tenant,p.domain_id);
 IF p.status<>'uncertain' OR length(trim(p_reason))<10 THEN RAISE EXCEPTION 'DNS_REVIEW_REQUIRED' USING ERRCODE='23514';END IF;
 UPDATE dns_change_plans SET status='completed',fence=fence+1,lease_owner=NULL,lease_until=NULL,last_error_code='READ_ONLY_OBSERVATION_CONFIRMED',updated_at=now() WHERE tenant_id=p_tenant AND id=p_id;
 -- Unknown creation provenance stays unowned, so rollback cannot delete a
 -- record that only happened to match an uncertain request.
 UPDATE dns_record_actions SET status='applied',owned=false,last_error_code='MANUAL_OBSERVATION_UNOWNED',updated_at=now() WHERE tenant_id=p_tenant AND plan_id=p_id AND status IN('pending','submitted','uncertain');
END $$;
ALTER FUNCTION urmail_review_dns_plan(bigint,uuid,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_review_dns_plan(bigint,uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_review_dns_plan(bigint,uuid,text) TO urmail_app;
CREATE FUNCTION urmail_enqueue_verified_domain() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.status<>'active' THEN
 UPDATE dns_change_plans SET status=CASE WHEN status IN('draft','confirmed') THEN 'canceled' ELSE 'uncertain' END,fence=fence+1,lease_owner=NULL,lease_until=NULL,last_error_code='DNS_DOMAIN_DISABLED',updated_at=now() WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.id AND status IN('draft','confirmed','applying','rollback_pending');
 UPDATE onboarding_tasks SET status='blocked',fence=fence+1,leased_until=NULL,leased_by=NULL,last_error_code='DNS_DOMAIN_DISABLED',updated_at=now() WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.id AND status IN('pending','running');
 END IF;
 IF NEW.status='active' AND NEW.verified_at<=now() AND NEW.provisioning_state='pending'
 AND EXISTS(SELECT 1 FROM subscriptions WHERE tenant_id=NEW.tenant_id AND status IN('active','trialing','past_due') AND period_start<=now() AND period_end>now())
 AND NOT EXISTS(SELECT 1 FROM onboarding_tasks WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.id AND (status IN('pending','running') OR (status='blocked' AND (coalesce(last_error_code,'')||coalesce(last_error,'')) LIKE '%RESTORE_RECONCILIATION_REQUIRED%'))) THEN
 INSERT INTO onboarding_tasks(tenant_id,domain_id,status,step,max_attempts) VALUES(NEW.tenant_id,NEW.id,'pending','bootstrap_config',12);
 END IF;RETURN NEW;
END $$;
ALTER FUNCTION urmail_enqueue_verified_domain() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_enqueue_verified_domain() FROM PUBLIC;
CREATE TRIGGER enqueue_verified_domain AFTER UPDATE OF status,verified_at,mode,dkim_selector,dkim_private_key ON domains FOR EACH ROW EXECUTE FUNCTION urmail_enqueue_verified_domain();
CREATE FUNCTION urmail_abandon_dns_plan(p_tenant bigint,p_id uuid,p_reason text) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE p dns_change_plans%ROWTYPE;BEGIN
 SELECT * INTO p FROM dns_change_plans WHERE tenant_id=p_tenant AND id=p_id FOR UPDATE;IF NOT FOUND THEN RAISE EXCEPTION 'DNS_PLAN_NOT_FOUND' USING ERRCODE='23514';END IF;
 PERFORM urmail_dns_actor(p_tenant,p.domain_id);
 IF p.status NOT IN('uncertain','failed') OR length(trim(p_reason))<10 THEN RAISE EXCEPTION 'DNS_REVIEW_REQUIRED' USING ERRCODE='23514';END IF;
 -- An explicit abandonment makes no claim about unknown external outcomes.
 -- Journals remain intact; a new preview must inspect current upstream state.
 UPDATE dns_change_plans SET status='failed',fence=fence+1,lease_owner=NULL,lease_until=NULL,last_error_code='ABANDONED_WITH_UNRESOLVED_EXTERNAL_FACTS',updated_at=now() WHERE tenant_id=p_tenant AND id=p_id;
END $$;
ALTER FUNCTION urmail_abandon_dns_plan(bigint,uuid,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_abandon_dns_plan(bigint,uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_abandon_dns_plan(bigint,uuid,text) TO urmail_app;
DO $$ DECLARE signature text;definition text;BEGIN
 FOREACH signature IN ARRAY ARRAY['urmail_schedule_marketing(bigint,bigint,timestamptz)','urmail_authorize_marketing_send(bigint,bigint,text,bigint)'] LOOP
 SELECT pg_get_functiondef(signature::regprocedure) INTO definition;
 definition:=replace(definition,'AND d.status=''active''','AND d.status=''active'' AND /* onboarding_marketing_sender_gate */ d.provisioning_state=''ready''');
 IF strpos(definition,'onboarding_marketing_sender_gate')=0 THEN RAISE EXCEPTION 'MARKETING_ONBOARDING_GATE_UPDATE_REQUIRED';END IF;EXECUTE definition;
 END LOOP;
END $$;
