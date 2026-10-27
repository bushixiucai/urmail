-- Archive investigations.
-- Membership is the read boundary. Removing a member removes access even if
-- the user remains a tenant administrator. A case never grants every mailbox.

CREATE TABLE archive_investigations (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 title TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
 purpose TEXT NOT NULL CHECK (length(purpose) BETWEEN 1 AND 2000),
 status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed')),
 created_by BIGINT NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE (tenant_id, id)
);
CREATE TABLE archive_investigation_members (
 investigation_id BIGINT NOT NULL,
 tenant_id BIGINT NOT NULL,
 user_id BIGINT NOT NULL,
 removed_at TIMESTAMPTZ,
 PRIMARY KEY (investigation_id, user_id),
 FOREIGN KEY (tenant_id, investigation_id) REFERENCES archive_investigations(tenant_id, id) ON DELETE CASCADE
);
CREATE TABLE archive_investigation_sources (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 investigation_id BIGINT NOT NULL,
 tenant_id BIGINT NOT NULL,
 archive_entry_id BIGINT NOT NULL,
 added_by BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE (investigation_id, archive_entry_id),
 FOREIGN KEY (tenant_id, investigation_id) REFERENCES archive_investigations(tenant_id, id) ON DELETE CASCADE,
 FOREIGN KEY (tenant_id, archive_entry_id) REFERENCES archive_entries(tenant_id, id)
);
ALTER TABLE archive_investigations ENABLE ROW LEVEL SECURITY;
ALTER TABLE archive_investigations FORCE ROW LEVEL SECURITY;
ALTER TABLE archive_investigation_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE archive_investigation_members FORCE ROW LEVEL SECURITY;
ALTER TABLE archive_investigation_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE archive_investigation_sources FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON archive_investigations USING (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint) WITH CHECK (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint);
CREATE POLICY tenant_isolation ON archive_investigation_members USING (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint) WITH CHECK (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint);
CREATE POLICY tenant_isolation ON archive_investigation_sources USING (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint) WITH CHECK (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint);
GRANT SELECT, INSERT, UPDATE, DELETE ON archive_investigations, archive_investigation_members, archive_investigation_sources TO urmail_app, urmail_platform;

-- The platform runtime role owns maintenance tables (restore quarantine,
-- fixture maintenance, TRUNCATE); same contract as 0028.
ALTER TABLE archive_investigations OWNER TO urmail_platform;
ALTER TABLE archive_investigation_members OWNER TO urmail_platform;
ALTER TABLE archive_investigation_sources OWNER TO urmail_platform;
