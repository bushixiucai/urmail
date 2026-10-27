-- Store one evidence manifest body only when its SHA-256 matches the approved digest.
-- Visibility is checked again so a removed member cannot store it.

ALTER TABLE archive_approval_requests ADD COLUMN evidence_body BYTEA CHECK (octet_length(evidence_body) <= 1000000);

CREATE FUNCTION urmail_store_evidence_body(p_tenant bigint, p_request bigint, p_actor bigint, p_digest text, p_body bytea) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE row archive_approval_requests;
BEGIN
 IF p_digest !~ '^[0-9a-f]{64}$' OR p_body IS NULL OR octet_length(p_body)=0 OR octet_length(p_body)>1000000 THEN RETURN 'invalid'; END IF;
 IF encode(sha256(p_body), 'hex') <> p_digest THEN RETURN 'digest_mismatch'; END IF;
 IF NOT urmail_evidence_manifest_visible(p_tenant, p_request, p_actor, p_digest) THEN RETURN 'not_visible'; END IF;
 SELECT * INTO row FROM archive_approval_requests WHERE tenant_id=p_tenant AND id=p_request FOR UPDATE;
 IF row.evidence_body IS NOT NULL AND encode(sha256(row.evidence_body), 'hex') <> p_digest THEN RETURN 'manifest_mismatch'; END IF;
 UPDATE archive_approval_requests SET evidence_body=p_body WHERE id=row.id;
 RETURN 'ok';
END $$;
ALTER FUNCTION urmail_store_evidence_body(bigint,bigint,bigint,text,bytea) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_store_evidence_body(bigint,bigint,bigint,text,bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_store_evidence_body(bigint,bigint,bigint,text,bytea) TO urmail_platform;
