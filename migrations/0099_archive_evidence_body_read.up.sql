-- Return one stored evidence body only after visibility is checked again.

CREATE FUNCTION urmail_read_evidence_body(p_tenant bigint, p_request bigint, p_actor bigint, p_digest text) RETURNS bytea
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE body bytea;
BEGIN
 IF NOT urmail_evidence_manifest_visible(p_tenant, p_request, p_actor, p_digest) THEN RETURN NULL; END IF;
 SELECT evidence_body INTO body FROM archive_approval_requests
 WHERE tenant_id=p_tenant AND id=p_request AND evidence_digest=p_digest AND encode(sha256(evidence_body), 'hex')=p_digest;
 RETURN body;
END $$;
ALTER FUNCTION urmail_read_evidence_body(bigint,bigint,bigint,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_read_evidence_body(bigint,bigint,bigint,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_read_evidence_body(bigint,bigint,bigint,text) TO urmail_app, urmail_platform;
