-- Credentials and the durable mailbox-principal budget ledger are service-only.
CREATE TABLE ai_provider_configs (
 tenant_id bigint PRIMARY KEY REFERENCES tenants(id),
 mode text NOT NULL CHECK(mode IN('platform','byo','local')),
 provider text NOT NULL CHECK(provider IN('openai','azure','ollama')),
 endpoint text NOT NULL,
 model text NOT NULL,
 encrypted_key text NOT NULL DEFAULT '',
 enabled boolean NOT NULL DEFAULT false,
 local_only boolean NOT NULL DEFAULT true,
 input_micros_per_million bigint NOT NULL DEFAULT 0 CHECK(input_micros_per_million BETWEEN 0 AND 9007199254740991),
 output_micros_per_million bigint NOT NULL DEFAULT 0 CHECK(output_micros_per_million BETWEEN 0 AND 9007199254740991),
 cost_currency text NOT NULL DEFAULT 'CNY' CHECK(cost_currency IN('CNY','USD')),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE ai_usage_requests (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 mailbox_id bigint NOT NULL,
 request_key text NOT NULL CHECK(length(request_key) BETWEEN 16 AND 200),
 input_digest text NOT NULL CHECK(input_digest ~ '^[a-f0-9]{64}$'),
 period_start timestamptz NOT NULL,
 period_end timestamptz NOT NULL CHECK(period_end>period_start),
 mode text NOT NULL CHECK(mode IN('platform','byo','local')),
 provider text NOT NULL,
 model text NOT NULL,
 cost_currency text NOT NULL CHECK(cost_currency IN('CNY','USD')),
 input_rate bigint NOT NULL CHECK(input_rate>=0),
 output_rate bigint NOT NULL CHECK(output_rate>=0),
 status text NOT NULL DEFAULT 'reserved' CHECK(status IN('reserved','completed','uncertain','released','resolved')),
 reserved_tokens bigint NOT NULL CHECK(reserved_tokens>0),
 reserved_cost_micros bigint NOT NULL CHECK(reserved_cost_micros BETWEEN 0 AND 9007199254740991),
 actual_tokens bigint CHECK(actual_tokens>=0),
 actual_cost_micros bigint CHECK(actual_cost_micros BETWEEN 0 AND 9007199254740991),
 result jsonb,
 review_reason text,
 reviewed_by bigint REFERENCES users(id),
 reviewed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,mailbox_id) REFERENCES mailboxes(tenant_id,id),
 UNIQUE(tenant_id,request_key)
);
ALTER TABLE ai_provider_configs OWNER TO urmail_platform;
ALTER TABLE ai_usage_requests OWNER TO urmail_platform;
ALTER TABLE ai_provider_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_usage_requests ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON ai_provider_configs,ai_usage_requests FROM PUBLIC,urmail_app,urmail_dovecot,urmail_roundcube;
CREATE INDEX ON ai_usage_requests(tenant_id,period_start,status);
-- Tenant writes share the panel business transaction and its successful audit.
CREATE FUNCTION urmail_configure_tenant_ai(p_mode text,p_provider text,p_endpoint text,p_model text,p_key text,p_enabled boolean,p_local_only boolean) RETURNS void
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
 INSERT INTO ai_provider_configs(tenant_id,mode,provider,endpoint,model,encrypted_key,enabled,local_only)
 VALUES(tid,p_mode,p_provider,p_endpoint,p_model,p_key,p_enabled,p_local_only)
 ON CONFLICT(tenant_id) DO UPDATE SET mode=EXCLUDED.mode,provider=EXCLUDED.provider,endpoint=EXCLUDED.endpoint,model=EXCLUDED.model,encrypted_key=EXCLUDED.encrypted_key,enabled=EXCLUDED.enabled,local_only=EXCLUDED.local_only,input_micros_per_million=0,output_micros_per_million=0,updated_at=now();
END $$;
ALTER FUNCTION urmail_configure_tenant_ai(text,text,text,text,text,boolean,boolean) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_configure_tenant_ai(text,text,text,text,text,boolean,boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_configure_tenant_ai(text,text,text,text,text,boolean,boolean) TO urmail_app;
REVOKE ALL ON SEQUENCE ai_usage_requests_id_seq FROM PUBLIC,urmail_app,urmail_dovecot,urmail_roundcube;
