-- Reverse 0008_rls_hardening.

DROP POLICY IF EXISTS tickets_isolation ON tickets;
DROP INDEX IF EXISTS tickets_tenant_created_idx;
ALTER TABLE tickets DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS audit_logs_no_delete ON audit_logs;
DROP POLICY IF EXISTS channels_no_delete ON channels;
DROP POLICY IF EXISTS announcements_no_delete ON announcements;

GRANT INSERT, UPDATE, DELETE ON users TO urmail_app;
GRANT INSERT, UPDATE, DELETE ON user_tenant_roles TO urmail_app;
GRANT INSERT, UPDATE, DELETE ON tenants TO urmail_app;
GRANT INSERT, UPDATE, DELETE ON audit_logs TO urmail_app;
GRANT INSERT, UPDATE, DELETE ON channels, announcements TO urmail_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON sessions TO urmail_app;
