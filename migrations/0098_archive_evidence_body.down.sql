DROP FUNCTION IF EXISTS urmail_store_evidence_body(bigint,bigint,bigint,text,bytea);
ALTER TABLE archive_approval_requests DROP COLUMN IF EXISTS evidence_body;
