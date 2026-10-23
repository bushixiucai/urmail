-- Short-lived MFA authorization and separately revocable legacy credentials.
ALTER TABLE sessions ADD COLUMN mfa_verified_at TIMESTAMPTZ,
 ADD COLUMN kind TEXT NOT NULL DEFAULT 'panel' CHECK (kind IN ('panel','legacy'));
CREATE TABLE application_credentials (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    token_hash TEXT NOT NULL UNIQUE CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    scopes TEXT[] NOT NULL CHECK (cardinality(scopes) > 0 AND scopes <@ ARRAY['legacy:read','legacy:write']::text[]),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at)
);
CREATE INDEX ON application_credentials (user_id);
ALTER TABLE application_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE application_credentials FORCE ROW LEVEL SECURITY;
ALTER TABLE application_credentials OWNER TO urmail_platform;
REVOKE ALL ON application_credentials FROM PUBLIC, urmail_app, urmail_dovecot;
REVOKE ALL ON SEQUENCE application_credentials_id_seq FROM PUBLIC, urmail_app, urmail_dovecot;

-- Only platform administrators may authorize hosts; untrusted Host values do
-- not create bindings. Tenant branding links carry no authentication authority.
CREATE TABLE brand_hosts (
    hostname TEXT PRIMARY KEY CHECK (hostname = lower(hostname) AND length(hostname) BETWEEN 1 AND 253),
    brand_id BIGINT NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE brand_hosts ENABLE ROW LEVEL SECURITY;
ALTER TABLE brand_hosts FORCE ROW LEVEL SECURITY;
ALTER TABLE brand_hosts OWNER TO urmail_platform;
REVOKE ALL ON brand_hosts FROM PUBLIC, urmail_app, urmail_dovecot;
