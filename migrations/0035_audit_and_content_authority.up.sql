-- Successful audit rows share the business transaction; failure rows are
-- written by the trusted platform sink after that transaction has rolled back.
CREATE FUNCTION urmail_record_audit(p_tenant bigint,p_user bigint,p_ip inet,p_action text,p_target text,p_status integer)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint
 OR p_user IS DISTINCT FROM nullif(current_setting('urmail.user_id',true),'')::bigint
 OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=p_tenant AND user_id=p_user)
 THEN RAISE EXCEPTION 'invalid audit scope' USING ERRCODE='42501'; END IF;
 IF p_action IS NULL OR p_action !~ '^[A-Za-z]{3,10} [A-Za-z0-9/{}:._~-]{0,120}$'
 OR p_target IS NULL OR length(p_target)>2048 OR p_status<100 OR p_status>599
 THEN RAISE EXCEPTION 'invalid audit metadata' USING ERRCODE='22023'; END IF;
 INSERT INTO audit_logs(tenant_id,actor_user_id,actor_ip,action,target_type,target_id,detail)
 VALUES(p_tenant,p_user,p_ip,p_action,'http',p_target,
 jsonb_build_object('phase','transaction','status',p_status,'outcome',CASE WHEN p_status>=400 THEN 'failed' ELSE 'succeeded' END));
END $$;
ALTER FUNCTION urmail_record_audit(bigint,bigint,inet,text,text,integer) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_record_audit(bigint,bigint,inet,text,text,integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_record_audit(bigint,bigint,inet,text,text,integer) TO urmail_app;
CREATE OR REPLACE FUNCTION urmail_record_audit(p_tenant bigint,p_user bigint,p_ip inet,p_action text,p_target text)
RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT urmail_record_audit(p_tenant,p_user,p_ip,p_action,p_target,200);
$$;
ALTER FUNCTION urmail_record_audit(bigint,bigint,inet,text,text) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_record_audit(bigint,bigint,inet,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_record_audit(bigint,bigint,inet,text,text) TO urmail_app;

-- Existing grants are not silently blessed: only a new, explicit owner
-- authorization has this timestamp. Historical rows remain available to review.
ALTER TABLE compliance_grants ADD COLUMN owner_confirmed_at timestamptz;
CREATE FUNCTION urmail_require_content_owner() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tid bigint; actor bigint;
BEGIN
 tid:=CASE WHEN TG_OP='DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
 actor:=nullif(current_setting('urmail.user_id',true),'')::bigint;
 IF tid IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint
 OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=tid AND user_id=actor AND role='owner')
 THEN RAISE EXCEPTION 'COMPLIANCE_OWNER_REQUIRED' USING ERRCODE='42501'; END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 IF TG_OP='UPDATE' THEN
  IF (NEW.tenant_id,NEW.user_id,NEW.scope,NEW.expires_at,NEW.reason,NEW.granted_by,NEW.owner_confirmed_at)
    IS DISTINCT FROM (OLD.tenant_id,OLD.user_id,OLD.scope,OLD.expires_at,OLD.reason,OLD.granted_by,OLD.owner_confirmed_at)
    OR NOT NEW.revoked
  THEN RAISE EXCEPTION 'COMPLIANCE_GRANT_IMMUTABLE' USING ERRCODE='42501'; END IF;
  IF OLD.revoked THEN RETURN OLD; END IF;
  NEW.revoked_by:=actor; NEW.revoked_at:=now(); NEW.updated_at:=now();
  RETURN NEW;
 END IF;
 IF NEW.scope IN ('full_content','export') AND
  (NEW.expires_at IS NULL OR NEW.expires_at<=now() OR length(btrim(NEW.reason))=0 OR length(NEW.reason)>2000
   OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=tid AND user_id=NEW.user_id AND role IN('owner','admin')))
 THEN RAISE EXCEPTION 'COMPLIANCE_GRANT_INVALID' USING ERRCODE='23514'; END IF;
 IF NEW.granted_by IS NOT NULL AND NEW.granted_by<>actor THEN
  RAISE EXCEPTION 'COMPLIANCE_GRANT_ISSUER_INVALID' USING ERRCODE='42501'; END IF;
 NEW.granted_by:=actor; NEW.owner_confirmed_at:=now();
 NEW.created_at:=now(); NEW.updated_at:=now(); NEW.revoked:=false; NEW.revoked_by:=NULL; NEW.revoked_at:=NULL;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_require_content_owner() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_require_content_owner() FROM PUBLIC;
DROP TRIGGER compliance_grants_content_admin_guard ON compliance_grants;
CREATE TRIGGER compliance_grants_content_owner_guard BEFORE INSERT OR UPDATE OR DELETE ON compliance_grants
 FOR EACH ROW EXECUTE FUNCTION urmail_require_content_owner();

-- Authorizations are revoked, never erased by the tenant application role.
REVOKE DELETE ON compliance_grants FROM urmail_app;
