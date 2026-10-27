-- Migration 0124 (MAIL-008): per-domain mail transport security state.
--
-- Stores the MTA-STS policy identity and TLS-RPT report destination per
-- tenant domain so the well-known body (mta-sts.<domain>/.well-known/
-- mta-sts.txt) and the rendered _mta-sts/_smtp._tls TXT records stay stable
-- across reconcile passes instead of being re-derived with a drifting id.
--
-- Secrets are not stored here: the table only carries public DNS-facing
-- values. Completeness reporting itself is computed on demand by
-- internal/maildnsreport (pure assessment over observed DNS records).

CREATE TABLE domain_transport_security (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 domain_id bigint NOT NULL REFERENCES domains(id),
 -- MTA-STS policy id rendered in _mta-sts TXT (id=...) and the well-known
 -- policy; stable per publication, changed only when the policy changes.
 mtasts_policy_id text NOT NULL CHECK(length(btrim(mtasts_policy_id)) BETWEEN 1 AND 32),
 mtasts_mode text NOT NULL DEFAULT 'enforce' CHECK(mtasts_mode IN ('enforce','testing','none')),
 tls_rpt_rua text NOT NULL CHECK(length(btrim(tls_rpt_rua)) BETWEEN 3 AND 320),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(tenant_id, domain_id),
 -- domain must belong to the tenant: cross-tenant rows are unrepresentable.
 FOREIGN KEY(tenant_id, domain_id) REFERENCES domains(tenant_id, id)
);

DO $$
DECLARE n text;
BEGIN
 FOREACH n IN ARRAY ARRAY['domain_transport_security'] LOOP
 EXECUTE format('ALTER TABLE %I OWNER TO urmail_platform',n);
 EXECUTE format('REVOKE ALL ON %I FROM PUBLIC,urmail_app,urmail_dovecot',n);
 EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',n);
 EXECUTE format('CREATE POLICY tenant_isolation ON %I USING(tenant_id=nullif(current_setting(''urmail.tenant_id'',true),'''')::bigint)',n);
 END LOOP;
END $$;

REVOKE ALL ON SEQUENCE domain_transport_security_id_seq FROM PUBLIC,urmail_app,urmail_dovecot;
GRANT SELECT(id,tenant_id,domain_id,mtasts_policy_id,mtasts_mode,tls_rpt_rua,created_at,updated_at) ON domain_transport_security TO urmail_app;

-- The platform runtime role owns maintenance tables (restore quarantine,
-- fixture maintenance, TRUNCATE); same contract as 0028.
ALTER TABLE domain_transport_security OWNER TO urmail_platform;
