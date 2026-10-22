-- 0008_rls_hardening: close the remaining tenant isolation gaps.
--
-- The application role (urmail_app) is tenant-scoped. It must never be able
-- to read or mutate another tenant's rows, and reference/audit data must not
-- be writable from a tenant context.
--
-- This migration:
--   1. puts tickets under RLS (it was missing from 0001),
--   2. makes audit_logs / channels / announcements append-only from the app
--      role (DELETE was still possible because 0004 only restricted
--      INSERT/UPDATE),
--   3. makes users / user_tenant_roles / tenants read-only for the app role
--      (role and tenant lifecycle moves to the platform role),
--   4. removes app access to sessions (authentication already runs on the
--      platform pool; expired-session cleanup must use the platform pool).

-- 1. Tickets were missing from the 0001 RLS table list.
ALTER TABLE tickets ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tickets_isolation ON tickets;
CREATE POLICY tickets_isolation ON tickets
    USING (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    WITH CHECK (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint);

CREATE INDEX IF NOT EXISTS tickets_tenant_created_idx
    ON tickets (tenant_id, created_at DESC);

-- 2. Audit records are append-only from the tenant side. INSERT/UPDATE were
-- already blocked by 0004; DELETE was not.
DROP POLICY IF EXISTS audit_logs_no_delete ON audit_logs;
CREATE POLICY audit_logs_no_delete ON audit_logs AS RESTRICTIVE
    FOR DELETE USING (false);

-- Global/brand channels and announcements are reference data. Tenants may
-- read them; they may never modify or delete them.
DROP POLICY IF EXISTS channels_no_delete ON channels;
CREATE POLICY channels_no_delete ON channels AS RESTRICTIVE
    FOR DELETE USING (false);
DROP POLICY IF EXISTS announcements_no_delete ON announcements;
CREATE POLICY announcements_no_delete ON announcements AS RESTRICTIVE
    FOR DELETE USING (false);

-- 3. Tenant/user/role lifecycle is platform-only. Keep SELECT for the
-- tenant UI and the role lookup used by RequireTenantTx.
REVOKE INSERT, UPDATE, DELETE ON users FROM urmail_app;
REVOKE INSERT, UPDATE, DELETE ON user_tenant_roles FROM urmail_app;
REVOKE INSERT, UPDATE, DELETE ON tenants FROM urmail_app;
REVOKE INSERT, UPDATE, DELETE ON audit_logs FROM urmail_app;
REVOKE INSERT, UPDATE, DELETE ON channels, announcements FROM urmail_app;

-- 4. The app role does not need sessions at all. Logout/authentication use
-- the platform pool; purgeExpiredSessions is switched to the platform pool.
REVOKE SELECT, INSERT, UPDATE, DELETE ON sessions FROM urmail_app;
