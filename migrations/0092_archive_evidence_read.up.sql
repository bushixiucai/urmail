-- Read authorization for one approved evidence manifest.
-- Membership and the approved source digest are checked again at read time.

CREATE FUNCTION urmail_evidence_manifest_visible(p_tenant bigint, p_request bigint, p_actor bigint, p_digest text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT EXISTS(
  SELECT 1 FROM archive_approval_requests r
  JOIN archive_investigation_members m ON m.tenant_id=r.tenant_id AND m.investigation_id=r.investigation_id
  WHERE r.tenant_id=p_tenant AND r.id=p_request AND r.state='approved' AND r.expires_at>now()
   AND r.evidence_digest=p_digest AND m.user_id=p_actor AND m.removed_at IS NULL
 );
$$;
ALTER FUNCTION urmail_evidence_manifest_visible(bigint,bigint,bigint,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_evidence_manifest_visible(bigint,bigint,bigint,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_evidence_manifest_visible(bigint,bigint,bigint,text) TO urmail_app, urmail_platform;
