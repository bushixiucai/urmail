-- MAIL-008: explicitly published transport policies and bounded TLS report facts.
ALTER TABLE domain_transport_security
 ALTER COLUMN mtasts_mode SET DEFAULT 'testing',
 ADD COLUMN enabled boolean NOT NULL DEFAULT false,
 ADD COLUMN policy_mx text[] NOT NULL DEFAULT '{}',
 ADD COLUMN max_age integer NOT NULL DEFAULT 86400 CHECK(max_age BETWEEN 300 AND 604800),
 ADD COLUMN published_at timestamptz,
 ADD COLUMN testing_since timestamptz,
 ADD COLUMN cache_safe_after timestamptz,
 ADD COLUMN observation_digest text,
 ADD COLUMN observed_at timestamptz,
 ADD COLUMN observation_status text NOT NULL DEFAULT 'unverified',
 ADD COLUMN observation_detail jsonb NOT NULL DEFAULT '{}',
 ADD COLUMN version bigint NOT NULL DEFAULT 1;
ALTER TABLE domain_transport_security FORCE ROW LEVEL SECURITY;
CREATE POLICY transport_platform ON domain_transport_security TO urmail_platform USING(true) WITH CHECK(true);
DROP POLICY tenant_isolation ON domain_transport_security;
CREATE POLICY transport_tenant_read ON domain_transport_security FOR SELECT TO urmail_app
 USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
CREATE POLICY transport_tenant_write ON domain_transport_security FOR ALL TO urmail_app
 USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND EXISTS(SELECT 1 FROM user_tenant_roles r WHERE r.tenant_id=domain_transport_security.tenant_id AND r.user_id=nullif(current_setting('urmail.user_id',true),'')::bigint AND r.role IN('owner','admin')))
 WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND EXISTS(SELECT 1 FROM user_tenant_roles r WHERE r.tenant_id=domain_transport_security.tenant_id AND r.user_id=nullif(current_setting('urmail.user_id',true),'')::bigint AND r.role IN('owner','admin')));
GRANT SELECT,INSERT,UPDATE ON domain_transport_security TO urmail_app;
GRANT USAGE,SELECT ON SEQUENCE domain_transport_security_id_seq TO urmail_app;

CREATE TABLE domain_tls_reports (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 domain_id bigint NOT NULL,
 report_digest text NOT NULL CHECK(length(report_digest)=64),
 reporter_digest text NOT NULL CHECK(length(reporter_digest)=64),
 report_id_digest text NOT NULL CHECK(length(report_id_digest)=64),
 period_start timestamptz NOT NULL,
 period_end timestamptz NOT NULL CHECK(period_end>=period_start AND period_end<=period_start+interval '8 days'),
 successful bigint NOT NULL CHECK(successful BETWEEN 0 AND 1000000000),
 failed bigint NOT NULL CHECK(failed BETWEEN 0 AND 1000000000),
 failures jsonb NOT NULL,
 received_at timestamptz NOT NULL DEFAULT now(),
 -- HTTPS submission alone does not authenticate the reporting organization.
 source_verified boolean NOT NULL DEFAULT false CHECK(NOT source_verified),
 UNIQUE(tenant_id,domain_id,reporter_digest,report_id_digest,period_start,period_end),
 FOREIGN KEY(tenant_id,domain_id) REFERENCES domains(tenant_id,id)
);
ALTER TABLE domain_tls_reports OWNER TO urmail_platform;
ALTER TABLE domain_tls_reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE domain_tls_reports FORCE ROW LEVEL SECURITY;
REVOKE ALL ON domain_tls_reports FROM PUBLIC,urmail_app,urmail_dovecot;
CREATE POLICY tls_report_platform ON domain_tls_reports TO urmail_platform USING(true) WITH CHECK(true);
CREATE POLICY tls_report_tenant_read ON domain_tls_reports FOR SELECT TO urmail_app
 USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND EXISTS(SELECT 1 FROM user_tenant_roles r WHERE r.tenant_id=domain_tls_reports.tenant_id AND r.user_id=nullif(current_setting('urmail.user_id',true),'')::bigint AND r.role IN('owner','admin')));
GRANT SELECT ON domain_tls_reports TO urmail_app;
