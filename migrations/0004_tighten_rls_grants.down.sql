-- Revert the M3 P0-3 tightening.

DROP POLICY IF EXISTS announcements_update_read_only ON announcements;
DROP POLICY IF EXISTS announcements_read_only ON announcements;
DROP POLICY IF EXISTS channels_update_read_only ON channels;
DROP POLICY IF EXISTS channels_read_only ON channels;

GRANT INSERT, UPDATE, DELETE ON coupons, plans TO urmail_app;

DROP POLICY IF EXISTS audit_logs_isolation ON audit_logs;
ALTER TABLE audit_logs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE audit_logs DISABLE ROW LEVEL SECURITY;
