DROP FUNCTION IF EXISTS urmail_store_evidence_manifest(bigint,bigint,bigint,text,text);
ALTER TABLE archive_approval_requests DROP COLUMN IF EXISTS evidence_digest;
