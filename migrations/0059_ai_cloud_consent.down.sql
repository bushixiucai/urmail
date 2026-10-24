DO $$ BEGIN IF EXISTS(SELECT 1 FROM ai_provider_configs WHERE enabled AND provider<>'ollama') THEN RAISE EXCEPTION 'AI_CLOUD_CONSENT_DOWNGRADE_REQUIRES_DISABLED_PROVIDERS';END IF;END $$;
DROP FUNCTION urmail_ai_cloud_authorized(bigint,text,text);
DROP FUNCTION urmail_consent_tenant_ai(text,text,boolean);
DROP TRIGGER ai_configuration_consent_guard ON ai_provider_configs;
DROP FUNCTION urmail_ai_configuration_consent_guard();
ALTER TABLE ai_provider_configs DROP COLUMN cloud_consent_revoked_at,DROP COLUMN cloud_consent_at,DROP COLUMN cloud_consent_by,DROP COLUMN cloud_consent_scope_hash,DROP COLUMN cloud_consent_digest,DROP COLUMN tenant_local_only,DROP COLUMN configuration_digest,DROP COLUMN cloud_scope_hash,DROP COLUMN configuration_version;
