-- OPS-101: brand-host ownership verification and TLS lifecycle closure.
-- A bound brand host starts as pending-verify; the platform operator proves
-- control of the hostname with a single-use DNS TXT challenge before the host
-- may receive a certificate or serve brand traffic.

ALTER TABLE brand_hosts
  ADD COLUMN status TEXT NOT NULL DEFAULT 'pending-verify'
    CHECK (status IN ('pending-verify','verified','cert-issued','active','failed')),
  ADD COLUMN failure_reason TEXT,
  ADD COLUMN verified_at TIMESTAMPTZ,
  ADD COLUMN cert_issued_at TIMESTAMPTZ,
  ADD COLUMN activated_at TIMESTAMPTZ,
  ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Only the SHA-256 digest of the challenge token is persisted; the raw token
-- is shown once to the operator and never stored. A consumed challenge can
-- never be replayed, and an expired one must be reissued.
CREATE TABLE brand_host_challenges (
    hostname TEXT PRIMARY KEY REFERENCES brand_hosts(hostname) ON DELETE CASCADE,
    token_digest TEXT NOT NULL CHECK (token_digest ~ '^[0-9a-f]{64}$'),
    method TEXT NOT NULL DEFAULT 'dns-txt' CHECK (method = 'dns-txt'),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at)
);
ALTER TABLE brand_host_challenges ENABLE ROW LEVEL SECURITY;
ALTER TABLE brand_host_challenges FORCE ROW LEVEL SECURITY;
ALTER TABLE brand_host_challenges OWNER TO urmail_platform;
REVOKE ALL ON brand_host_challenges FROM PUBLIC, urmail_app, urmail_dovecot;
