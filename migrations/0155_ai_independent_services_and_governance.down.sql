-- Migration 0155 rollback: Independent AI configurations and BYOK governance

DROP FUNCTION IF EXISTS urmail_configure_tenant_ai_governance(text, text[]);

CREATE OR REPLACE FUNCTION urmail_configure_tenant_ai(p_mode text,p_provider text,p_endpoint text,p_model text,p_key text,p_enabled boolean,p_local_only boolean) RETURNS void
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
 UPDATE ai_provider_configs SET mode=p_mode,provider=p_provider,endpoint=p_endpoint,model=p_model,encrypted_key=CASE WHEN p_key<>'' THEN p_key ELSE encrypted_key END,enabled=p_enabled,local_only=p_local_only,updated_at=now() WHERE tenant_id=tid;
 IF NOT FOUND THEN
  INSERT INTO ai_provider_configs(tenant_id,mode,provider,endpoint,model,encrypted_key,enabled,local_only)
  VALUES(tid,p_mode,p_provider,p_endpoint,p_model,p_key,p_enabled,p_local_only);
 END IF;
END $$;

ALTER TABLE ai_provider_configs
  DROP COLUMN IF EXISTS platform_provider,
  DROP COLUMN IF EXISTS platform_endpoint,
  DROP COLUMN IF EXISTS platform_model,
  DROP COLUMN IF EXISTS platform_encrypted_key,
  DROP COLUMN IF EXISTS platform_enabled,
  DROP COLUMN IF EXISTS enterprise_mode,
  DROP COLUMN IF EXISTS enterprise_provider,
  DROP COLUMN IF EXISTS enterprise_endpoint,
  DROP COLUMN IF EXISTS enterprise_model,
  DROP COLUMN IF EXISTS enterprise_encrypted_key,
  DROP COLUMN IF EXISTS enterprise_enabled,
  DROP COLUMN IF EXISTS enterprise_local_only,
  DROP COLUMN IF EXISTS personal_mode,
  DROP COLUMN IF EXISTS approved_endpoints,
  DROP COLUMN IF EXISTS active_service;
