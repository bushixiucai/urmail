-- Migration 0028: compliance grants, retention policies, and compliance search
-- indexes on the archive content metadata table (archive_entries).
--
-- pg_trgm is an UNTRUSTED extension: only a superuser can create it, and the
-- migration role (urmail_migrate) is deliberately NOLOGIN/NOSUPERUSER. The
-- extension is therefore created by the installer's superuser stage in
-- internal/db/bootstrap.sql BEFORE migrations run. The DO blocks below only
-- create the indexes when the extension is already present; they never try to
-- install it and never fail the migration when it is missing (search then
-- falls back to the existing tsvector index).

CREATE TABLE compliance_grants (
    id          BIGSERIAL PRIMARY KEY,
    tenant_id   BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope       TEXT NOT NULL DEFAULT 'metadata_only',
    expires_at  TIMESTAMPTZ,
    revoked     BOOLEAN NOT NULL DEFAULT false,
    reason      TEXT NOT NULL DEFAULT '',
    -- Provenance: who granted, and who revoked. A tenant member must never be
    -- able to create or hold a content/export grant (see the guard trigger).
    granted_by  BIGINT REFERENCES users(id) ON DELETE SET NULL,
    revoked_by  BIGINT REFERENCES users(id) ON DELETE SET NULL,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT compliance_grants_scope_check
        CHECK (scope IN ('metadata_only', 'full_content', 'export'))
);

CREATE INDEX ON compliance_grants (tenant_id, user_id, revoked);
CREATE INDEX compliance_grants_active_expiry_idx ON compliance_grants (tenant_id, expires_at) WHERE revoked = false;

ALTER TABLE compliance_grants ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON compliance_grants
    USING (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    WITH CHECK (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint);

GRANT SELECT, INSERT, UPDATE, DELETE ON compliance_grants TO urmail_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON compliance_grants TO urmail_platform;

-- Retention policies table
CREATE TABLE retention_policies (
    id             BIGSERIAL PRIMARY KEY,
    tenant_id      BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    retention_days INT NOT NULL CHECK (retention_days > 0),
    action         TEXT NOT NULL DEFAULT 'soft_delete',
    active         BOOLEAN NOT NULL DEFAULT true,
    created_by     BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT retention_policies_action_check
        CHECK (action IN ('soft_delete', 'hard_delete', 'archive_export'))
);

CREATE UNIQUE INDEX ON retention_policies (tenant_id);

ALTER TABLE retention_policies ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON retention_policies
    USING (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    WITH CHECK (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint);

GRANT SELECT, INSERT, UPDATE, DELETE ON retention_policies TO urmail_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON retention_policies TO urmail_platform;

-- Grant guard: content grants and retention policies are destructive
-- privileges. RLS already pins every write to the caller's own tenant; this
-- trigger additionally requires the acting USER to hold the owner or admin
-- tenant role, so a plain member cannot grant themselves export scope or arm
-- a hard_delete policy (M6 review defect D6). The platform connection is the
-- administrative path and is recognised by its login role, not by a GUC it
-- could share with a tenant session.
--
-- The function is only reachable as a trigger: it performs no writes of its
-- own and reads role membership for the tenant RLS already validated, so it
-- grants no new capability to any role.
CREATE FUNCTION urmail_require_tenant_content_admin() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public AS $$
BEGIN
    IF session_user = 'urmail_platform' THEN
        RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM user_tenant_roles
        WHERE tenant_id = CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END
          AND user_id = nullif(current_setting('urmail.user_id', true), '')::bigint
          AND role IN ('owner', 'admin')
    ) THEN
        RAISE EXCEPTION 'COMPLIANCE_GRANT_ROLE_REQUIRED' USING ERRCODE = '42501';
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END
$$;
ALTER FUNCTION urmail_require_tenant_content_admin() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_require_tenant_content_admin() FROM PUBLIC;

CREATE TRIGGER compliance_grants_content_admin_guard
    BEFORE INSERT OR UPDATE OR DELETE ON compliance_grants
    FOR EACH ROW EXECUTE FUNCTION urmail_require_tenant_content_admin();

CREATE TRIGGER retention_policies_content_admin_guard
    BEFORE INSERT OR UPDATE OR DELETE ON retention_policies
    FOR EACH ROW EXECUTE FUNCTION urmail_require_tenant_content_admin();

-- Compliance retrieval indexes over archive content metadata.
-- pg_trgm powers fast substring/similarity search over subject, sender and
-- message-id; the received_at index supports retention/cleanup range scans.
-- The extension is installed by bootstrap.sql (superuser stage); when it is
-- absent these indexes are skipped and search degrades to the tsvector path.
CREATE INDEX IF NOT EXISTS archive_entries_tenant_received_idx
    ON archive_entries (tenant_id, deleted_at, received_at DESC);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
        EXECUTE 'CREATE INDEX IF NOT EXISTS archive_entries_subject_trgm_idx
            ON archive_entries USING gin (subject gin_trgm_ops)';
        EXECUTE 'CREATE INDEX IF NOT EXISTS archive_entries_from_addr_trgm_idx
            ON archive_entries USING gin (from_addr gin_trgm_ops)';
        EXECUTE 'CREATE INDEX IF NOT EXISTS archive_entries_message_id_trgm_idx
            ON archive_entries USING gin (message_id gin_trgm_ops)';
    ELSE
        RAISE NOTICE 'pg_trgm not installed (see internal/db/bootstrap.sql); skipping trigram indexes';
    END IF;
END $$;

-- Align with 0001: the platform role owns every table so it holds the full
-- privilege set (including TRUNCATE) and bypasses the tenant RLS policies.
ALTER TABLE compliance_grants OWNER TO urmail_platform;
ALTER TABLE retention_policies OWNER TO urmail_platform;
