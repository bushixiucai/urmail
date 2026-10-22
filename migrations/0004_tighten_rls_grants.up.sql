-- Tighten RLS and grants (M3 P0-3).
--
-- 1. audit_logs gains row level security: the app role may only read rows of
--    its own tenant and may never write (WITH CHECK (false)). Audit records
--    are produced by the platform role, which owns the tables and has
--    BYPASSRLS, so FORCE ROW LEVEL SECURITY does not affect it.
-- 2. coupons and plans are platform-managed reference data: the app role
--    keeps SELECT but loses INSERT/UPDATE/DELETE.
-- 3. channels and announcements were readable but writable by the app role
--    (their policies had no WITH CHECK, which defaults to the USING clause).
--    Restrictive INSERT/UPDATE policies make them read-only for the app role.

ALTER TABLE audit_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_logs FORCE ROW LEVEL SECURITY;
CREATE POLICY audit_logs_isolation ON audit_logs
    USING (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
    WITH CHECK (false);

REVOKE INSERT, UPDATE, DELETE ON coupons, plans FROM urmail_app;

CREATE POLICY channels_read_only ON channels AS RESTRICTIVE
    FOR INSERT WITH CHECK (false);
CREATE POLICY channels_update_read_only ON channels AS RESTRICTIVE
    FOR UPDATE WITH CHECK (false);
CREATE POLICY announcements_read_only ON announcements AS RESTRICTIVE
    FOR INSERT WITH CHECK (false);
CREATE POLICY announcements_update_read_only ON announcements AS RESTRICTIVE
    FOR UPDATE WITH CHECK (false);
