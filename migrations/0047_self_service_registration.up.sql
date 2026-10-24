CREATE TABLE registration_requests (
 token_hash text PRIMARY KEY,
 email text NOT NULL,
 password_hash text NOT NULL,
 tenant_name text NOT NULL,
 brand_id bigint NOT NULL REFERENCES brands(id),
 expires_at timestamptz NOT NULL,
 consumed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE registration_requests OWNER TO urmail_platform;
REVOKE ALL ON registration_requests FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE registration_requests ENABLE ROW LEVEL SECURITY;
CREATE POLICY no_app_access ON registration_requests USING(false);
CREATE INDEX ON registration_requests(email,created_at);
CREATE TABLE tenant_contacts (
 tenant_id bigint PRIMARY KEY REFERENCES tenants(id),
 email text NOT NULL,
 verified_at timestamptz NOT NULL,
 registration_source text NOT NULL DEFAULT 'self_service' CHECK(registration_source='self_service')
);
ALTER TABLE tenant_contacts OWNER TO urmail_platform;
REVOKE ALL ON tenant_contacts FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER TABLE tenant_contacts ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_contacts USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
GRANT SELECT ON tenant_contacts TO urmail_app;
ALTER TABLE notification_outbox ADD COLUMN secret_payload text;
ALTER TABLE notification_outbox DROP CONSTRAINT notification_outbox_check;
ALTER TABLE notification_outbox ADD CHECK(channel<>'email' OR (recipient IS NOT NULL AND (recipient_verified OR kind='contact_verification')));
-- Pending self-service tenants may prove one domain without obtaining mail
-- service or a free subscription. Existing gifted free tenants are untouched.
DO $$ DECLARE definition text; BEGIN
 SELECT pg_get_functiondef('urmail_resource_guard()'::regprocedure) INTO definition;
 definition:=replace(definition,'SELECT * INTO s FROM subscriptions WHERE tenant_id=tid AND status IN(''active'',''trialing'',''past_due'');',
 'IF TG_TABLE_NAME=''domains'' AND TG_OP=''INSERT'' AND EXISTS(SELECT 1 FROM tenant_contacts tc JOIN tenants t ON t.id=tc.tenant_id WHERE tc.tenant_id=tid AND t.status=''pending'') AND NOT EXISTS(SELECT 1 FROM subscriptions WHERE tenant_id=tid AND status IN(''active'',''trialing'',''past_due'')) THEN
 IF (SELECT count(*) FROM domains WHERE tenant_id=tid)>=1 THEN RAISE EXCEPTION ''DOMAIN_QUOTA_EXCEEDED'' USING ERRCODE=''23514''; END IF; RETURN NEW; END IF;
 SELECT * INTO s FROM subscriptions WHERE tenant_id=tid AND status IN(''active'',''trialing'',''past_due'');');
 EXECUTE definition;
END $$;
