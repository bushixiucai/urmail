-- Complete the case/approval/export product boundary without broadening grants.
ALTER TABLE archive_investigations ADD COLUMN request_key text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX archive_case_request_key ON archive_investigations(tenant_id,created_by,request_key) WHERE request_key<>'';
ALTER TABLE archive_investigations ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK(version>0);
ALTER TABLE archive_investigations ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE archive_approval_requests ADD COLUMN case_version bigint NOT NULL DEFAULT 0;
ALTER TABLE archive_approval_requests ADD COLUMN source_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE archive_approval_requests ADD COLUMN decision_reason text NOT NULL DEFAULT '' CHECK(length(decision_reason)<=2000);

CREATE FUNCTION urmail_archive_case_grant(t bigint,u bigint,s text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT EXISTS(SELECT 1 FROM compliance_grants g JOIN user_tenant_roles r ON r.tenant_id=g.tenant_id AND r.user_id=g.user_id
 WHERE t=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND g.tenant_id=t AND g.user_id=u AND g.scope=s AND NOT g.revoked AND g.expires_at>statement_timestamp()
 AND g.owner_confirmed_at IS NOT NULL AND g.granted_by IS NOT NULL AND g.granted_by<>u AND length(btrim(g.reason))>0
 AND r.role IN('owner','admin'));
$$;
CREATE FUNCTION urmail_archive_case_member(t bigint,c bigint,u bigint) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT urmail_archive_case_grant(t,u,'full_content') AND EXISTS(
 SELECT 1 FROM archive_investigations i JOIN archive_investigation_members m ON m.tenant_id=i.tenant_id AND m.investigation_id=i.id
 WHERE i.tenant_id=t AND i.id=c AND i.expires_at>statement_timestamp() AND m.user_id=u AND m.removed_at IS NULL);
$$;
CREATE FUNCTION urmail_archive_case_manager(t bigint,c bigint,u bigint) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT urmail_archive_case_member(t,c,u) AND EXISTS(SELECT 1 FROM archive_investigations WHERE tenant_id=t AND id=c AND created_by=u AND status='open');
$$;
CREATE FUNCTION urmail_archive_case_snapshot(t bigint,c bigint,u bigint) RETURNS jsonb
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT CASE WHEN t=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND u=nullif(current_setting('urmail.user_id',true),'')::bigint AND urmail_archive_case_member(t,c,u) THEN jsonb_build_object(
 'case_id',i.id,'version',i.version,'expires_at',i.expires_at,'status',i.status,
 'sources',coalesce((SELECT jsonb_agg(jsonb_build_object('id',e.id,'sha256',e.raw_sha256,'size',e.size_bytes,'mailbox_id',e.mailbox_id,'deleted_at',e.deleted_at,'legal_hold',e.legal_hold) ORDER BY e.id)
 FROM archive_investigation_sources s JOIN archive_entries e ON e.tenant_id=s.tenant_id AND e.id=s.archive_entry_id WHERE s.tenant_id=t AND s.investigation_id=c),'[]'::jsonb),
 'members',coalesce((SELECT jsonb_agg(jsonb_build_object('user_id',m.user_id,'removed_at',m.removed_at,'grants',
  coalesce((SELECT jsonb_agg(jsonb_build_object('id',g.id,'scope',g.scope,'expires_at',g.expires_at,'grantor',g.granted_by,'reason',g.reason,'updated_at',g.updated_at,'role',r.role) ORDER BY g.id)
   FROM compliance_grants g JOIN user_tenant_roles r ON r.tenant_id=g.tenant_id AND r.user_id=g.user_id
   WHERE g.tenant_id=t AND g.user_id=m.user_id AND g.scope IN('full_content','export') AND NOT g.revoked AND g.expires_at>statement_timestamp()
   AND g.owner_confirmed_at IS NOT NULL AND g.granted_by IS NOT NULL AND g.granted_by<>g.user_id AND length(btrim(g.reason))>0 AND r.role IN('owner','admin')),'[]'::jsonb)) ORDER BY m.user_id)
 FROM archive_investigation_members m WHERE m.tenant_id=t AND m.investigation_id=c),'[]'::jsonb)) ELSE NULL END
 FROM archive_investigations i WHERE i.tenant_id=t AND i.id=c;
$$;
CREATE FUNCTION urmail_create_archive_case(t bigint,u bigint,title text,purpose text,expiry timestamptz,request_key text) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE result bigint;
BEGIN
 IF t IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint OR u IS DISTINCT FROM nullif(current_setting('urmail.user_id',true),'')::bigint
 OR request_key !~ '^[0-9a-f-]{36}$' OR NOT urmail_archive_case_grant(t,u,'full_content') OR expiry<=statement_timestamp() OR expiry>statement_timestamp()+interval '1 year' THEN RAISE EXCEPTION 'CASE_NOT_AUTHORIZED' USING ERRCODE='42501'; END IF;
 SELECT i.id INTO result FROM archive_investigations i WHERE i.tenant_id=t AND i.created_by=u AND i.request_key=urmail_create_archive_case.request_key;
 IF FOUND THEN
  IF NOT EXISTS(SELECT 1 FROM archive_investigations i WHERE i.id=result AND i.tenant_id=t AND i.title=urmail_create_archive_case.title AND i.purpose=urmail_create_archive_case.purpose AND i.expires_at=expiry) THEN RAISE EXCEPTION 'CASE_REQUEST_KEY_MISMATCH' USING ERRCODE='23514';END IF;
  RETURN result; END IF;
 INSERT INTO archive_investigations(tenant_id,title,purpose,created_by,expires_at,request_key) VALUES(t,title,purpose,u,expiry,request_key) RETURNING id INTO result;
 INSERT INTO archive_investigation_members(tenant_id,investigation_id,user_id) VALUES(t,result,u);
 RETURN result;
END $$;

CREATE FUNCTION urmail_archive_case_changed() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF TG_TABLE_NAME='archive_investigations' THEN NEW.version=OLD.version+1;NEW.updated_at=statement_timestamp();RETURN NEW;END IF;
 IF TG_OP='DELETE' THEN UPDATE archive_investigations SET updated_at=statement_timestamp() WHERE tenant_id=OLD.tenant_id AND id=OLD.investigation_id;RETURN OLD;END IF;
 UPDATE archive_investigations SET updated_at=statement_timestamp() WHERE tenant_id=NEW.tenant_id AND id=NEW.investigation_id;RETURN NEW;
END $$;
CREATE TRIGGER archive_case_version BEFORE UPDATE ON archive_investigations FOR EACH ROW EXECUTE FUNCTION urmail_archive_case_changed();
CREATE TRIGGER archive_case_members_changed AFTER INSERT OR UPDATE OR DELETE ON archive_investigation_members FOR EACH ROW EXECUTE FUNCTION urmail_archive_case_changed();
CREATE TRIGGER archive_case_sources_changed AFTER INSERT OR UPDATE OR DELETE ON archive_investigation_sources FOR EACH ROW EXECUTE FUNCTION urmail_archive_case_changed();

CREATE FUNCTION urmail_archive_approval_bound() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE snapshot jsonb;actor bigint:=nullif(current_setting('urmail.user_id',true),'')::bigint;
BEGIN
 -- Historical rows remain readable by their historical contract; new product
 -- requests must bind the live source/member/grant snapshot at insertion.
 IF TG_OP='UPDATE' AND OLD.case_version=0 THEN RAISE EXCEPTION 'LEGACY_APPROVAL_CANNOT_AUTHORIZE_PRODUCT_EXPORT' USING ERRCODE='23514';END IF;
 IF NEW.evidence_digest IS NOT NULL OR NEW.evidence_body IS NOT NULL THEN RAISE EXCEPTION 'PRODUCT_EVIDENCE_SEPARATE_BOUNDARY' USING ERRCODE='23514';END IF;
 IF TG_OP='UPDATE' AND (NEW.case_version<>OLD.case_version OR NEW.source_digest<>OLD.source_digest OR NEW.source_snapshot<>OLD.source_snapshot OR NEW.tenant_id<>OLD.tenant_id OR NEW.investigation_id<>OLD.investigation_id OR NEW.requester_user_id<>OLD.requester_user_id OR NEW.expires_at<>OLD.expires_at) THEN RAISE EXCEPTION 'APPROVAL_TARGET_IMMUTABLE' USING ERRCODE='23514';END IF;

 IF TG_OP='UPDATE' AND NEW.state='expired' AND OLD.state IN('pending','approved') AND actor IS NULL
 AND current_setting('role',true)='urmail_platform' AND NEW.decision_reason LIKE 'RESTORE_RECONCILIATION_REQUIRED:%' THEN RETURN NEW;END IF;
 snapshot=urmail_archive_case_snapshot(NEW.tenant_id,NEW.investigation_id,actor);
 IF snapshot IS NULL OR snapshot->>'status'<>'open' OR (snapshot->>'version')::bigint<>NEW.case_version
 OR encode(sha256(convert_to(snapshot::text,'UTF8')),'hex')<>NEW.source_digest OR NEW.source_snapshot<>snapshot
 OR NOT urmail_archive_case_grant(NEW.tenant_id,actor,'export') OR NOT urmail_archive_case_grant(NEW.tenant_id,NEW.requester_user_id,'export') THEN RAISE EXCEPTION 'APPROVAL_SNAPSHOT_STALE' USING ERRCODE='23514';END IF;
 IF TG_OP='INSERT' AND (NEW.state<>'pending' OR NEW.requester_user_id<>actor OR NEW.approver_user_id IS NOT NULL OR NEW.case_version<=0) THEN RAISE EXCEPTION 'APPROVAL_INVALID_REQUESTER' USING ERRCODE='23514';END IF;
 IF TG_OP='UPDATE' THEN
 IF OLD.state<>'pending' THEN RAISE EXCEPTION 'APPROVAL_CLOSED' USING ERRCODE='23514';END IF;
 IF NEW.state IN('approved','rejected') AND (NEW.requester_user_id=actor OR NEW.approver_user_id IS DISTINCT FROM actor OR NEW.decided_at IS NULL OR length(btrim(NEW.decision_reason))=0) THEN RAISE EXCEPTION 'APPROVAL_INDEPENDENT_ACTOR_REQUIRED' USING ERRCODE='23514';END IF;
 IF NEW.state='withdrawn' AND NEW.requester_user_id<>actor THEN RAISE EXCEPTION 'APPROVAL_NOT_REQUESTER' USING ERRCODE='23514';END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER archive_product_approval_insert BEFORE INSERT ON archive_approval_requests FOR EACH ROW WHEN(NEW.case_version>0) EXECUTE FUNCTION urmail_archive_approval_bound();
CREATE TRIGGER archive_product_approval_update BEFORE UPDATE ON archive_approval_requests FOR EACH ROW WHEN(NEW.case_version>0 OR OLD.case_version>0) EXECUTE FUNCTION urmail_archive_approval_bound();

CREATE TABLE archive_evidence_exports(
 tenant_id bigint NOT NULL,approval_id bigint NOT NULL,created_by bigint NOT NULL,
 manifest bytea NOT NULL CHECK(octet_length(manifest)>0 AND octet_length(manifest)<=1000000),
 manifest_sha256 text NOT NULL CHECK(manifest_sha256 ~ '^[0-9a-f]{64}$'),created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,approval_id),FOREIGN KEY(tenant_id,approval_id) REFERENCES archive_approval_requests(tenant_id,id),
 CHECK(encode(sha256(manifest),'hex')=manifest_sha256)
);
ALTER TABLE archive_evidence_exports OWNER TO urmail_platform;
ALTER TABLE archive_evidence_exports ENABLE ROW LEVEL SECURITY;
ALTER TABLE archive_evidence_exports FORCE ROW LEVEL SECURITY;
CREATE FUNCTION urmail_archive_export_visible(t bigint,a bigint,u bigint) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT EXISTS(SELECT 1 FROM archive_approval_requests r JOIN archive_investigations i ON i.tenant_id=r.tenant_id AND i.id=r.investigation_id
 WHERE t=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND u=nullif(current_setting('urmail.user_id',true),'')::bigint AND r.tenant_id=t AND r.id=a AND r.state='approved' AND r.expires_at>statement_timestamp() AND i.status='open' AND r.case_version=i.version
 AND r.requester_user_id<>r.approver_user_id AND urmail_archive_case_member(t,i.id,u) AND urmail_archive_case_grant(t,u,'export')
 AND urmail_archive_case_member(t,i.id,r.requester_user_id) AND urmail_archive_case_grant(t,r.requester_user_id,'export')
 AND urmail_archive_case_member(t,i.id,r.approver_user_id) AND urmail_archive_case_grant(t,r.approver_user_id,'export')
 AND r.source_snapshot=urmail_archive_case_snapshot(t,i.id,u));
$$;
CREATE POLICY case_evidence_visible ON archive_evidence_exports USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND urmail_archive_export_visible(tenant_id,approval_id,nullif(current_setting('urmail.user_id',true),'')::bigint))
 WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND created_by=nullif(current_setting('urmail.user_id',true),'')::bigint AND urmail_archive_export_visible(tenant_id,approval_id,created_by));
GRANT SELECT,INSERT ON archive_evidence_exports TO urmail_app,urmail_platform;

DROP POLICY tenant_isolation ON archive_investigations;
CREATE POLICY tenant_isolation ON archive_investigations USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND urmail_archive_case_member(tenant_id,id,nullif(current_setting('urmail.user_id',true),'')::bigint))
 WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND created_by=nullif(current_setting('urmail.user_id',true),'')::bigint AND urmail_archive_case_member(tenant_id,id,created_by));
DROP POLICY tenant_isolation ON archive_investigation_members;
CREATE POLICY tenant_isolation ON archive_investigation_members USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND urmail_archive_case_member(tenant_id,investigation_id,nullif(current_setting('urmail.user_id',true),'')::bigint))
 WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND urmail_archive_case_manager(tenant_id,investigation_id,nullif(current_setting('urmail.user_id',true),'')::bigint));
DROP POLICY tenant_isolation ON archive_investigation_sources;
CREATE POLICY tenant_isolation ON archive_investigation_sources USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND urmail_archive_case_member(tenant_id,investigation_id,nullif(current_setting('urmail.user_id',true),'')::bigint))
 WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND urmail_archive_case_member(tenant_id,investigation_id,nullif(current_setting('urmail.user_id',true),'')::bigint));
DROP POLICY tenant_isolation ON archive_approval_requests;
CREATE POLICY tenant_isolation ON archive_approval_requests USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND urmail_archive_case_member(tenant_id,investigation_id,nullif(current_setting('urmail.user_id',true),'')::bigint))
 WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND urmail_archive_case_member(tenant_id,investigation_id,nullif(current_setting('urmail.user_id',true),'')::bigint));

DO $$DECLARE f record;BEGIN FOR f IN SELECT oid::regprocedure signature FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname IN('urmail_archive_case_grant','urmail_archive_case_member','urmail_archive_case_manager','urmail_archive_case_snapshot','urmail_create_archive_case','urmail_archive_case_changed','urmail_archive_approval_bound','urmail_archive_export_visible') LOOP
 EXECUTE format('ALTER FUNCTION %s OWNER TO urmail_platform',f.signature);
 EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC',f.signature);
 IF f.signature::text NOT LIKE 'urmail_archive_case_changed(%' AND f.signature::text NOT LIKE 'urmail_archive_approval_bound(%' THEN EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO urmail_app,urmail_platform',f.signature);END IF;
END LOOP;END $$;
