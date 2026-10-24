-- Platform provider setup is distinct from the tenant's authority to export mail.
ALTER TABLE ai_provider_configs ADD COLUMN configuration_version bigint NOT NULL DEFAULT 1,
 ADD COLUMN cloud_scope_hash text NOT NULL DEFAULT '43a8dfa67f89e1fa6861bf0f3c283bce46f112511c472b36e3174e30d1b037fe',
 ADD COLUMN configuration_digest text NOT NULL DEFAULT '',
 ADD COLUMN tenant_local_only boolean NOT NULL DEFAULT true,
 ADD COLUMN cloud_consent_digest text NOT NULL DEFAULT '',
 ADD COLUMN cloud_consent_scope_hash text NOT NULL DEFAULT '',
 ADD COLUMN cloud_consent_by bigint REFERENCES users(id),
 ADD COLUMN cloud_consent_at timestamptz,
 ADD COLUMN cloud_consent_revoked_at timestamptz;
CREATE FUNCTION urmail_ai_configuration_consent_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE actor bigint:=nullif(current_setting('urmail.user_id',true),'')::bigint;
BEGIN
 IF TG_OP='UPDATE' THEN
  NEW.configuration_version:=OLD.configuration_version;
  IF (NEW.mode,NEW.provider,NEW.endpoint,NEW.model,NEW.cloud_scope_hash) IS DISTINCT FROM (OLD.mode,OLD.provider,OLD.endpoint,OLD.model,OLD.cloud_scope_hash) THEN
   IF OLD.configuration_version>=9007199254740991 THEN RAISE EXCEPTION 'AI_CONFIGURATION_VERSION_EXHAUSTED' USING ERRCODE='23514';END IF;
   NEW.configuration_version:=OLD.configuration_version+1;
  END IF;
 ELSE NEW.configuration_version:=1;NEW.tenant_local_only:=true;NEW.cloud_consent_digest:='';NEW.cloud_consent_scope_hash:='';NEW.cloud_consent_by:=NULL;NEW.cloud_consent_at:=NULL;NEW.cloud_consent_revoked_at:=NULL;END IF;
 IF TG_OP='UPDATE' AND NEW.mode='local' AND current_setting('role',true)='urmail_app' THEN NEW.tenant_local_only:=true;NEW.cloud_consent_digest:='';NEW.cloud_consent_scope_hash:='';NEW.cloud_consent_by:=actor;NEW.cloud_consent_at:=NULL;NEW.cloud_consent_revoked_at:=statement_timestamp();END IF;
 NEW.configuration_digest:=encode(sha256(convert_to(jsonb_build_array(NEW.configuration_version,NEW.mode,NEW.provider,NEW.endpoint,NEW.model,NEW.cloud_scope_hash)::text,'UTF8')),'hex');
 IF TG_OP='UPDATE' THEN
  IF (NEW.tenant_local_only,NEW.cloud_consent_digest,NEW.cloud_consent_scope_hash,NEW.cloud_consent_by,NEW.cloud_consent_at,NEW.cloud_consent_revoked_at) IS DISTINCT FROM (OLD.tenant_local_only,OLD.cloud_consent_digest,OLD.cloud_consent_scope_hash,OLD.cloud_consent_by,OLD.cloud_consent_at,OLD.cloud_consent_revoked_at) THEN
   IF current_setting('role',true)<>'urmail_app' OR NEW.tenant_id IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint OR NEW.cloud_consent_by IS DISTINCT FROM actor OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=NEW.tenant_id AND user_id=actor AND role IN('owner','admin')) THEN RAISE EXCEPTION 'AI_TENANT_CONSENT_REQUIRED' USING ERRCODE='42501';END IF;
   IF NOT NEW.tenant_local_only AND (NEW.cloud_consent_digest<>NEW.configuration_digest OR NEW.cloud_consent_scope_hash<>NEW.cloud_scope_hash OR NEW.cloud_consent_at IS NULL OR NEW.cloud_consent_revoked_at IS NOT NULL) THEN RAISE EXCEPTION 'AI_CLOUD_CONSENT_STALE' USING ERRCODE='23514';END IF;
  END IF;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_ai_configuration_consent_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_ai_configuration_consent_guard() FROM PUBLIC;
CREATE TRIGGER ai_configuration_consent_guard BEFORE INSERT OR UPDATE ON ai_provider_configs FOR EACH ROW EXECUTE FUNCTION urmail_ai_configuration_consent_guard();
UPDATE ai_provider_configs SET configuration_version=configuration_version;
CREATE FUNCTION urmail_consent_tenant_ai(p_digest text,p_scope text,p_allow boolean) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tid bigint:=nullif(current_setting('urmail.tenant_id',true),'')::bigint; actor bigint:=nullif(current_setting('urmail.user_id',true),'')::bigint; cfg ai_provider_configs%ROWTYPE;
BEGIN
 IF tid IS NULL OR actor IS NULL OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=tid AND user_id=actor AND role IN('owner','admin')) THEN RAISE EXCEPTION 'AI_CONFIGURATION_FORBIDDEN' USING ERRCODE='42501';END IF;
 PERFORM pg_advisory_xact_lock(tid);
 SELECT * INTO cfg FROM ai_provider_configs WHERE tenant_id=tid FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'AI_DISABLED' USING ERRCODE='23514';END IF;
 IF p_allow AND (cfg.provider='ollama' OR NOT cfg.enabled OR p_digest<>cfg.configuration_digest OR p_scope<>cfg.cloud_scope_hash) THEN RAISE EXCEPTION 'AI_CLOUD_CONSENT_STALE' USING ERRCODE='23514';END IF;
 UPDATE ai_provider_configs SET tenant_local_only=NOT p_allow,cloud_consent_digest=CASE WHEN p_allow THEN cfg.configuration_digest ELSE '' END,cloud_consent_scope_hash=CASE WHEN p_allow THEN cfg.cloud_scope_hash ELSE '' END,cloud_consent_by=actor,cloud_consent_at=CASE WHEN p_allow THEN statement_timestamp() ELSE NULL END,cloud_consent_revoked_at=CASE WHEN p_allow THEN NULL ELSE statement_timestamp() END WHERE tenant_id=tid;
END $$;
ALTER FUNCTION urmail_consent_tenant_ai(text,text,boolean) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_consent_tenant_ai(text,text,boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_consent_tenant_ai(text,text,boolean) TO urmail_app;
CREATE FUNCTION urmail_ai_cloud_authorized(p_tenant bigint,p_digest text,p_scope text) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE selected bigint;
BEGIN
 IF current_setting('role',true) NOT IN('urmail_platform','urmail_migrate') AND p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint THEN RETURN false;END IF;
 SELECT c.tenant_id INTO selected FROM ai_provider_configs c JOIN user_tenant_roles r ON r.tenant_id=c.tenant_id AND r.user_id=c.cloud_consent_by
 WHERE c.tenant_id=p_tenant AND c.enabled AND NOT c.tenant_local_only AND NOT c.local_only AND c.provider IN('openai','azure') AND c.configuration_digest=p_digest AND c.cloud_scope_hash=p_scope AND c.cloud_consent_digest=c.configuration_digest AND c.cloud_consent_scope_hash=c.cloud_scope_hash AND c.cloud_consent_at IS NOT NULL AND c.cloud_consent_revoked_at IS NULL AND r.role IN('owner','admin') FOR SHARE OF c,r;
 RETURN selected IS NOT NULL;
END $$;
ALTER FUNCTION urmail_ai_cloud_authorized(bigint,text,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_ai_cloud_authorized(bigint,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_ai_cloud_authorized(bigint,text,text) TO urmail_app;
