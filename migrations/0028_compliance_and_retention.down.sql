-- Migration 0028 down
DROP TABLE IF EXISTS retention_policies CASCADE;
DROP TABLE IF EXISTS compliance_grants CASCADE;
DROP FUNCTION IF EXISTS urmail_require_tenant_content_admin();

-- Drop the compliance search indexes added in 0028.
DROP INDEX IF EXISTS archive_entries_tenant_received_idx;
DROP INDEX IF EXISTS archive_entries_message_id_trgm_idx;
DROP INDEX IF EXISTS archive_entries_from_addr_trgm_idx;
DROP INDEX IF EXISTS archive_entries_subject_trgm_idx;
-- pg_trgm is NOT dropped here: the extension may pre-date this project on the
-- host cluster and was installed by the superuser stage (bootstrap.sql), so
-- removing it would destroy an unrelated dependency.
