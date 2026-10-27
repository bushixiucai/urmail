-- Evidence manifest bound to one approved source digest.
-- Storing the manifest does not grant access. Verification remains external.

ALTER TABLE archive_approval_requests ADD COLUMN evidence_digest TEXT CHECK (evidence_digest IS NULL OR evidence_digest ~ '^[0-9a-f]{64}$');

CREATE FUNCTION urmail_store_evidence_manifest(p_tenant bigint, p_request bigint, p_actor bigint, p_source_digest text, p_evidence_digest text) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE row archive_approval_requests; member boolean;
BEGIN
 IF p_source_digest !~ '^[0-9a-f]{64}$' OR p_evidence_digest !~ '^[0-9a-f]{64}$' THEN RETURN 'invalid'; END IF;
 SELECT * INTO row FROM archive_approval_requests WHERE tenant_id=p_tenant AND id=p_request FOR UPDATE;
 IF NOT FOUND THEN RETURN 'not_found'; END IF;
 IF row.state<>'approved' OR row.expires_at<=now() THEN RETURN 'closed'; END IF;
 IF row.source_digest<>p_source_digest THEN RETURN 'target_mismatch'; END IF;
 SELECT EXISTS(SELECT 1 FROM archive_investigation_members m WHERE m.tenant_id=row.tenant_id AND m.investigation_id=row.investigation_id AND m.user_id=p_actor AND m.removed_at IS NULL) INTO member;
 IF NOT member THEN RETURN 'not_authorized'; END IF;
 IF row.evidence_digest IS NOT NULL AND row.evidence_digest<>p_evidence_digest THEN RETURN 'manifest_mismatch'; END IF;
 UPDATE archive_approval_requests SET evidence_digest=p_evidence_digest WHERE id=row.id;
 RETURN 'ok';
END $$;
ALTER FUNCTION urmail_store_evidence_manifest(bigint,bigint,bigint,text,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_store_evidence_manifest(bigint,bigint,bigint,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_store_evidence_manifest(bigint,bigint,bigint,text,text) TO urmail_platform;
