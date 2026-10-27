-- Independent approval for one investigation source set.
-- The requester cannot approve. A decision is bound to the source digest,
-- so copying its id to another source set does not authorize that set.

CREATE TABLE archive_approval_requests (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 investigation_id BIGINT NOT NULL,
 requester_user_id BIGINT NOT NULL,
 action TEXT NOT NULL CHECK (action IN ('export')),
 source_digest TEXT NOT NULL CHECK (source_digest ~ '^[0-9a-f]{64}$'),
 state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','approved','rejected','withdrawn','expired')),
 approver_user_id BIGINT,
 decided_at TIMESTAMPTZ,
 expires_at TIMESTAMPTZ NOT NULL,
 UNIQUE (tenant_id, id),
 FOREIGN KEY (tenant_id, investigation_id) REFERENCES archive_investigations(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE archive_approval_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE archive_approval_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON archive_approval_requests USING (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint) WITH CHECK (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint);
GRANT SELECT, INSERT, UPDATE ON archive_approval_requests TO urmail_app, urmail_platform;

CREATE FUNCTION urmail_decide_archive_approval(p_tenant bigint, p_request bigint, p_actor bigint, p_decision text, p_digest text) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE row archive_approval_requests; member boolean;
BEGIN
 IF p_decision NOT IN ('approved','rejected') THEN RETURN 'invalid'; END IF;
 SELECT * INTO row FROM archive_approval_requests WHERE tenant_id=p_tenant AND id=p_request FOR UPDATE;
 IF NOT FOUND THEN RETURN 'not_found'; END IF;
 IF row.state<>'pending' OR row.expires_at<=now() THEN RETURN 'closed'; END IF;
 IF row.requester_user_id=p_actor THEN RETURN 'self_approval_denied'; END IF;
 IF row.source_digest<>p_digest THEN RETURN 'target_mismatch'; END IF;
 SELECT EXISTS(SELECT 1 FROM archive_investigation_members m WHERE m.tenant_id=row.tenant_id AND m.investigation_id=row.investigation_id AND m.user_id=p_actor AND m.removed_at IS NULL) INTO member;
 IF NOT member THEN RETURN 'not_authorized'; END IF;
 UPDATE archive_approval_requests SET state=p_decision, approver_user_id=p_actor, decided_at=now() WHERE id=row.id;
 RETURN 'ok';
END $$;
ALTER FUNCTION urmail_decide_archive_approval(bigint,bigint,bigint,text,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_decide_archive_approval(bigint,bigint,bigint,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_decide_archive_approval(bigint,bigint,bigint,text,text) TO urmail_platform;

-- The platform runtime role owns maintenance tables (restore quarantine,
-- fixture maintenance, TRUNCATE); same contract as 0028.
ALTER TABLE archive_approval_requests OWNER TO urmail_platform;
