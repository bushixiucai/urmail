-- Reading/exporting archived content never grants destructive retention authority.
ALTER TABLE compliance_grants DROP CONSTRAINT compliance_grants_scope_check;
ALTER TABLE compliance_grants ADD CONSTRAINT compliance_grants_scope_check CHECK(scope IN('metadata_only','full_content','export','retention_manage'));
ALTER TABLE compliance_grants ADD CONSTRAINT compliance_grants_tenant_id_unique UNIQUE(tenant_id,id);
ALTER TABLE retention_previews ADD COLUMN authorization_grant_id bigint;
ALTER TABLE retention_previews ADD CONSTRAINT retention_preview_authorization_tenant_fk FOREIGN KEY(tenant_id,authorization_grant_id) REFERENCES compliance_grants(tenant_id,id);
ALTER TABLE retention_policies ADD COLUMN authorization_grant_id bigint;
ALTER TABLE retention_policies ADD CONSTRAINT retention_authorization_tenant_fk FOREIGN KEY(tenant_id,authorization_grant_id) REFERENCES compliance_grants(tenant_id,id);
DO $$ DECLARE definition text;BEGIN
 SELECT pg_get_functiondef('urmail_require_content_owner()'::regprocedure) INTO definition;
 definition:=replace(definition,'''full_content'',''export''','''full_content'',''export'',''retention_manage''');
 EXECUTE definition;
END $$;
-- Old policies have no owner-issued retention grant. Preserve their previews and
-- configuration for review, but never invent authorization for historical work.
INSERT INTO audit_logs(tenant_id,action,target_type,target_id,detail)
SELECT tenant_id,'MIGRATE retention.authorization','retention_policies',id::text,
 '{"actor_type":"migration","status":200,"outcome":"disabled_missing_retention_grant"}'::jsonb
FROM retention_policies WHERE active;
ALTER TABLE retention_policies DISABLE TRIGGER retention_policies_content_admin_guard;
UPDATE retention_policies SET active=false WHERE active;
ALTER TABLE retention_policies ENABLE TRIGGER retention_policies_content_admin_guard;
CREATE FUNCTION urmail_retention_authorized(p_tenant bigint,p_actor bigint,p_grant bigint) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE selected bigint;
BEGIN
 IF current_setting('role',true) NOT IN('urmail_platform','urmail_migrate') AND p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint THEN RETURN false;END IF;
 SELECT g.id INTO selected FROM compliance_grants g JOIN user_tenant_roles r ON r.tenant_id=g.tenant_id AND r.user_id=g.user_id
 WHERE g.tenant_id=p_tenant AND g.id=p_grant AND g.user_id=p_actor AND g.scope='retention_manage'
 AND NOT g.revoked AND g.owner_confirmed_at IS NOT NULL AND g.owner_confirmed_at<=statement_timestamp()
 AND g.granted_by IS NOT NULL AND length(btrim(g.reason))>0 AND g.expires_at>statement_timestamp() AND r.role IN('owner','admin')
 FOR SHARE OF g,r;
 RETURN selected IS NOT NULL;
END $$;
ALTER FUNCTION urmail_retention_authorized(bigint,bigint,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_retention_authorized(bigint,bigint,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_retention_authorized(bigint,bigint,bigint) TO urmail_app;
CREATE FUNCTION urmail_retention_management_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE actor bigint;tid bigint;grant_id bigint;
BEGIN
 actor:=nullif(current_setting('urmail.user_id',true),'')::bigint;
 tid:=CASE WHEN TG_OP='DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
 IF current_setting('role',true) IN('urmail_platform','urmail_migrate') THEN
  IF TG_TABLE_NAME='retention_policies' THEN IF TG_OP<>'DELETE' THEN IF NEW.active AND NOT urmail_retention_authorized(tid,NEW.created_by,NEW.authorization_grant_id) THEN RAISE EXCEPTION 'RETENTION_MANAGE_REQUIRED' USING ERRCODE='42501';END IF;END IF;
   RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
  END IF;
  IF TG_OP='DELETE' THEN RETURN OLD;END IF;actor:=NEW.created_by;
 ELSIF tid IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint THEN RAISE EXCEPTION 'RETENTION_MANAGE_REQUIRED' USING ERRCODE='42501';END IF;
 IF NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=tid AND user_id=actor AND role IN('owner','admin')) THEN RAISE EXCEPTION 'RETENTION_MANAGE_REQUIRED' USING ERRCODE='42501';END IF;
 -- Administrators can always stop new cleanup, including after grant expiry.
 IF TG_TABLE_NAME='retention_policies' THEN IF TG_OP='UPDATE' THEN IF NOT NEW.active AND (NEW.tenant_id,NEW.retention_days,NEW.action,NEW.created_by,NEW.confirmed_preview,NEW.authorization_grant_id) IS NOT DISTINCT FROM (OLD.tenant_id,OLD.retention_days,OLD.action,OLD.created_by,OLD.confirmed_preview,OLD.authorization_grant_id) THEN RETURN NEW;END IF;END IF;END IF;
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'RETENTION_POLICY_IMMUTABLE' USING ERRCODE='42501';END IF;
 IF NEW.created_by IS DISTINCT FROM actor THEN RAISE EXCEPTION 'RETENTION_MANAGE_REQUIRED' USING ERRCODE='42501';END IF;
 IF TG_TABLE_NAME='retention_policies' OR TG_OP='UPDATE' THEN grant_id:=NEW.authorization_grant_id;
 ELSE SELECT id INTO grant_id FROM compliance_grants WHERE tenant_id=tid AND user_id=actor AND scope='retention_manage' AND NOT revoked AND owner_confirmed_at IS NOT NULL AND expires_at>statement_timestamp() ORDER BY id DESC LIMIT 1;END IF;
 IF NOT urmail_retention_authorized(tid,actor,grant_id) THEN RAISE EXCEPTION 'RETENTION_MANAGE_REQUIRED' USING ERRCODE='42501';END IF;
 IF TG_TABLE_NAME='retention_previews' THEN
  IF TG_OP='INSERT' THEN
   NEW.authorization_grant_id:=grant_id;
   NEW.created_at:=statement_timestamp();NEW.expires_at:=statement_timestamp()+interval '15 minutes';NEW.confirmed_at:=NULL;
   SELECT count(*),md5(coalesce(string_agg(id::text,',' ORDER BY id),'')) INTO NEW.candidate_count,NEW.candidate_digest FROM archive_entries WHERE tenant_id=tid AND NOT legal_hold AND deleted_at IS NULL AND received_at<NEW.created_at-make_interval(days=>NEW.retention_days);
  ELSIF (NEW.id,NEW.tenant_id,NEW.retention_days,NEW.candidate_count,NEW.candidate_digest,NEW.created_by,NEW.created_at,NEW.expires_at,NEW.authorization_grant_id) IS DISTINCT FROM (OLD.id,OLD.tenant_id,OLD.retention_days,OLD.candidate_count,OLD.candidate_digest,OLD.created_by,OLD.created_at,OLD.expires_at,OLD.authorization_grant_id) OR OLD.confirmed_at IS NOT NULL OR NEW.confirmed_at IS NULL OR OLD.expires_at<=statement_timestamp() THEN RAISE EXCEPTION 'RETENTION_PREVIEW_STALE' USING ERRCODE='23514';
  ELSE NEW.confirmed_at:=statement_timestamp();END IF;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_retention_management_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_retention_management_guard() FROM PUBLIC;
DROP TRIGGER retention_policies_content_admin_guard ON retention_policies;
CREATE TRIGGER retention_management_guard BEFORE INSERT OR UPDATE OR DELETE ON retention_policies FOR EACH ROW EXECUTE FUNCTION urmail_retention_management_guard();
CREATE TRIGGER retention_preview_management_guard BEFORE INSERT OR UPDATE OR DELETE ON retention_previews FOR EACH ROW EXECUTE FUNCTION urmail_retention_management_guard();

CREATE OR REPLACE FUNCTION urmail_retention_confirmation_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.active AND NOT EXISTS(SELECT 1 FROM retention_previews p WHERE p.id=NEW.confirmed_preview AND p.tenant_id=NEW.tenant_id AND p.retention_days=NEW.retention_days AND p.created_by=NEW.created_by AND p.authorization_grant_id=NEW.authorization_grant_id AND p.confirmed_at IS NOT NULL AND p.expires_at>statement_timestamp()) THEN RAISE EXCEPTION 'RETENTION_PREVIEW_REQUIRED' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
