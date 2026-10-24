-- Do not erase the provenance of owner-issued destructive authorizations.
DO $$ BEGIN IF EXISTS(SELECT 1 FROM compliance_grants WHERE scope='retention_manage') THEN RAISE EXCEPTION 'RETENTION_MANAGE_GRANTS_REQUIRE_FORWARD_MIGRATION';END IF;END $$;
DROP TRIGGER retention_preview_management_guard ON retention_previews;
DROP TRIGGER retention_management_guard ON retention_policies;
CREATE TRIGGER retention_policies_content_admin_guard BEFORE INSERT OR UPDATE OR DELETE ON retention_policies FOR EACH ROW EXECUTE FUNCTION urmail_require_tenant_content_admin();
DROP FUNCTION urmail_retention_management_guard();
DROP FUNCTION urmail_retention_authorized(bigint,bigint,bigint);
CREATE OR REPLACE FUNCTION urmail_retention_confirmation_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$ BEGIN IF NEW.active AND NOT EXISTS(SELECT 1 FROM retention_previews p WHERE p.id=NEW.confirmed_preview AND p.tenant_id=NEW.tenant_id AND p.retention_days=NEW.retention_days AND p.confirmed_at IS NOT NULL) THEN RAISE EXCEPTION 'RETENTION_PREVIEW_REQUIRED' USING ERRCODE='23514'; END IF;RETURN NEW;END $$;
ALTER TABLE retention_policies DROP CONSTRAINT retention_authorization_tenant_fk;
ALTER TABLE retention_policies DROP COLUMN authorization_grant_id;
ALTER TABLE retention_previews DROP CONSTRAINT retention_preview_authorization_tenant_fk;
ALTER TABLE retention_previews DROP COLUMN authorization_grant_id;
ALTER TABLE compliance_grants DROP CONSTRAINT compliance_grants_tenant_id_unique;
ALTER TABLE compliance_grants DROP CONSTRAINT compliance_grants_scope_check;
ALTER TABLE compliance_grants ADD CONSTRAINT compliance_grants_scope_check CHECK(scope IN('metadata_only','full_content','export'));
DO $$ DECLARE definition text;BEGIN SELECT pg_get_functiondef('urmail_require_content_owner()'::regprocedure) INTO definition;definition:=replace(definition,'''full_content'',''export'',''retention_manage''','''full_content'',''export''');EXECUTE definition;END $$;
