-- Migration 0155: Independent AI configurations (Platform vs Enterprise vs Personal) and BYOK governance

ALTER TABLE ai_provider_configs
  ADD COLUMN platform_provider text NOT NULL DEFAULT 'openai' CHECK(platform_provider IN('openai','azure')),
  ADD COLUMN platform_endpoint text NOT NULL DEFAULT '',
  ADD COLUMN platform_model text NOT NULL DEFAULT '',
  ADD COLUMN platform_encrypted_key text NOT NULL DEFAULT '',
  ADD COLUMN platform_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN enterprise_mode text NOT NULL DEFAULT 'byo' CHECK(enterprise_mode IN('byo','local')),
  ADD COLUMN enterprise_provider text NOT NULL DEFAULT 'openai' CHECK(enterprise_provider IN('openai','azure','ollama')),
  ADD COLUMN enterprise_endpoint text NOT NULL DEFAULT '',
  ADD COLUMN enterprise_model text NOT NULL DEFAULT '',
  ADD COLUMN enterprise_encrypted_key text NOT NULL DEFAULT '',
  ADD COLUMN enterprise_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN enterprise_local_only boolean NOT NULL DEFAULT true,
  ADD COLUMN personal_mode text NOT NULL DEFAULT 'public' CHECK(personal_mode IN('disabled','approved','public')),
  ADD COLUMN approved_endpoints text[] NOT NULL DEFAULT '{}',
  ADD COLUMN active_service text NOT NULL DEFAULT 'enterprise' CHECK(active_service IN('platform','enterprise'));

UPDATE ai_provider_configs SET
  platform_provider = CASE WHEN mode = 'platform' THEN provider ELSE 'openai' END,
  platform_endpoint = CASE WHEN mode = 'platform' THEN endpoint ELSE '' END,
  platform_model = CASE WHEN mode = 'platform' THEN model ELSE '' END,
  platform_encrypted_key = CASE WHEN mode = 'platform' THEN encrypted_key ELSE '' END,
  platform_enabled = CASE WHEN mode = 'platform' THEN enabled ELSE false END,
  enterprise_mode = CASE WHEN mode IN ('byo', 'local') THEN mode ELSE 'byo' END,
  enterprise_provider = CASE WHEN mode IN ('byo', 'local') THEN provider ELSE 'openai' END,
  enterprise_endpoint = CASE WHEN mode IN ('byo', 'local') THEN endpoint ELSE '' END,
  enterprise_model = CASE WHEN mode IN ('byo', 'local') THEN model ELSE '' END,
  enterprise_encrypted_key = CASE WHEN mode IN ('byo', 'local') THEN encrypted_key ELSE '' END,
  enterprise_enabled = CASE WHEN mode IN ('byo', 'local') THEN enabled ELSE false END,
  enterprise_local_only = CASE WHEN mode IN ('byo', 'local') THEN local_only ELSE true END,
  active_service = CASE WHEN mode = 'platform' THEN 'platform' ELSE 'enterprise' END;

CREATE OR REPLACE FUNCTION urmail_configure_tenant_ai(
  p_mode text,
  p_provider text,
  p_endpoint text,
  p_model text,
  p_key text,
  p_enabled boolean,
  p_local_only boolean
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tid bigint:=nullif(current_setting('urmail.tenant_id',true),'')::bigint;
 uid bigint:=nullif(current_setting('urmail.user_id',true),'')::bigint;
BEGIN
 IF tid IS NULL OR uid IS NULL OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=tid AND user_id=uid AND role IN('owner','admin')) THEN
  RAISE EXCEPTION 'AI_CONFIGURATION_FORBIDDEN' USING ERRCODE='42501';
 END IF;
 IF p_mode NOT IN('byo','local') OR (p_mode='local')<>(p_provider='ollama') OR (p_local_only AND p_provider<>'ollama') THEN
  RAISE EXCEPTION 'AI_CONFIGURATION_INVALID' USING ERRCODE='23514';
 END IF;
 INSERT INTO ai_provider_configs(
   tenant_id,
   mode,
   provider,
   endpoint,
   model,
   encrypted_key,
   enabled,
   local_only,
   enterprise_mode,
   enterprise_provider,
   enterprise_endpoint,
   enterprise_model,
   enterprise_encrypted_key,
   enterprise_enabled,
   enterprise_local_only,
   active_service,
   updated_at
 )
 VALUES(
   tid,
   p_mode,
   p_provider,
   p_endpoint,
   p_model,
   p_key,
   p_enabled,
   p_local_only,
   p_mode,
   p_provider,
   p_endpoint,
   p_model,
   p_key,
   p_enabled,
   p_local_only,
   'enterprise',
   now()
 )
 ON CONFLICT(tenant_id) DO UPDATE SET
   enterprise_mode=p_mode,
   enterprise_provider=p_provider,
   enterprise_endpoint=p_endpoint,
   enterprise_model=p_model,
   enterprise_encrypted_key=CASE WHEN p_key<>'' THEN p_key ELSE ai_provider_configs.enterprise_encrypted_key END,
   enterprise_enabled=p_enabled,
   enterprise_local_only=p_local_only,
   active_service='enterprise',
   mode=p_mode,
   provider=p_provider,
   endpoint=p_endpoint,
   model=p_model,
   encrypted_key=CASE WHEN p_key<>'' THEN p_key ELSE ai_provider_configs.enterprise_encrypted_key END,
   enabled=p_enabled,
   local_only=p_local_only,
   updated_at=now();
END $$;

CREATE OR REPLACE FUNCTION urmail_configure_tenant_ai_governance(
  p_personal_mode text,
  p_approved_endpoints text[]
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tid bigint:=nullif(current_setting('urmail.tenant_id',true),'')::bigint;
 uid bigint:=nullif(current_setting('urmail.user_id',true),'')::bigint;
BEGIN
 IF tid IS NULL OR uid IS NULL OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=tid AND user_id=uid AND role IN('owner','admin')) THEN
  RAISE EXCEPTION 'AI_CONFIGURATION_FORBIDDEN' USING ERRCODE='42501';
 END IF;
 IF p_personal_mode NOT IN('disabled','approved','public') THEN
  RAISE EXCEPTION 'AI_GOVERNANCE_INVALID' USING ERRCODE='23514';
 END IF;
 INSERT INTO ai_provider_configs(
   tenant_id,
   mode,
   provider,
   endpoint,
   model,
   encrypted_key,
   enabled,
   local_only,
   personal_mode,
   approved_endpoints,
   updated_at
 )
 VALUES(
   tid,
   'byo',
   'openai',
   '',
   '',
   '',
   false,
   true,
   p_personal_mode,
   COALESCE(p_approved_endpoints, '{}'::text[]),
   now()
 )
 ON CONFLICT(tenant_id) DO UPDATE SET
   personal_mode=p_personal_mode,
   approved_endpoints=COALESCE(p_approved_endpoints, '{}'::text[]),
   updated_at=now();
END $$;
ALTER FUNCTION urmail_configure_tenant_ai_governance(text, text[]) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_configure_tenant_ai_governance(text, text[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_configure_tenant_ai_governance(text, text[]) TO urmail_app;
