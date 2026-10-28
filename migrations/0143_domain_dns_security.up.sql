-- Authoritative DNS intent is tenant-bound; private DNSSEC/TSIG material stays
-- exclusively in the root agent's protected key directory.
CREATE TABLE domain_dns_security (
 tenant_id bigint NOT NULL,
 domain_id bigint NOT NULL,
 dnssec_enabled boolean NOT NULL DEFAULT false,
 secondary_hostname text NOT NULL DEFAULT '',
 secondary_address text NOT NULL DEFAULT '',
 primary_address text NOT NULL DEFAULT '',
 tsig_key_name text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,domain_id),
 FOREIGN KEY(tenant_id,domain_id) REFERENCES domains(tenant_id,id),
 CHECK (length(secondary_hostname)<=253 AND length(secondary_address)<=64 AND length(primary_address)<=64 AND length(tsig_key_name)<=128),
 CHECK ((secondary_hostname='' AND secondary_address='' AND primary_address='' AND tsig_key_name='') OR (secondary_hostname<>'' AND secondary_address<>'' AND primary_address<>'' AND tsig_key_name<>''))
);
ALTER TABLE domain_dns_security OWNER TO urmail_platform;
ALTER TABLE domain_dns_security ENABLE ROW LEVEL SECURITY;
ALTER TABLE domain_dns_security FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON domain_dns_security USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint) WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
REVOKE ALL ON domain_dns_security FROM PUBLIC,urmail_app,urmail_dovecot;
GRANT SELECT ON domain_dns_security TO urmail_app;
