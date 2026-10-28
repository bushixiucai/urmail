DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM domain_transport_security WHERE enabled) OR EXISTS(SELECT 1 FROM domain_tls_reports) THEN
  RAISE EXCEPTION 'TRANSPORT_SECURITY_DOWN_REQUIRES_POLICY_CACHE_DRAIN_AND_REPORT_EXPORT';
 END IF;
END $$;
DROP TABLE domain_tls_reports;
DROP POLICY transport_platform ON domain_transport_security;
DROP POLICY transport_tenant_read ON domain_transport_security;
DROP POLICY transport_tenant_write ON domain_transport_security;
REVOKE INSERT,UPDATE ON domain_transport_security FROM urmail_app;
REVOKE USAGE,SELECT ON SEQUENCE domain_transport_security_id_seq FROM urmail_app;
CREATE POLICY tenant_isolation ON domain_transport_security
 USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint);
ALTER TABLE domain_transport_security NO FORCE ROW LEVEL SECURITY;
ALTER TABLE domain_transport_security ALTER COLUMN mtasts_mode SET DEFAULT 'enforce',
 DROP COLUMN enabled,DROP COLUMN policy_mx,DROP COLUMN max_age,DROP COLUMN published_at,
 DROP COLUMN testing_since,DROP COLUMN cache_safe_after,DROP COLUMN observation_digest,
 DROP COLUMN observed_at,DROP COLUMN observation_status,DROP COLUMN observation_detail,DROP COLUMN version;
