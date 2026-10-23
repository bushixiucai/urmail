CREATE TABLE dns_zone_versions (
    domain text PRIMARY KEY,
    content_hash text NOT NULL CHECK (length(content_hash) = 64),
    serial bigint NOT NULL CHECK (serial >= 0 AND serial < 4294967296),
    updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE dns_zone_versions OWNER TO urmail_platform;
GRANT SELECT, INSERT, UPDATE, DELETE ON dns_zone_versions TO urmail_platform;
REVOKE ALL ON dns_zone_versions FROM PUBLIC, urmail_app, urmail_dovecot;
