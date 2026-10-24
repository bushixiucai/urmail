-- Preserve distinct originals that happen to share a Message-ID.
ALTER TABLE archive_entries ADD COLUMN raw_sha256 text NOT NULL DEFAULT '';
ALTER TABLE archive_entries DROP CONSTRAINT archive_entries_tenant_mailbox_message_uniq;
ALTER TABLE archive_entries ADD CONSTRAINT archive_entries_tenant_mailbox_message_uniq UNIQUE(tenant_id,mailbox_id,message_id,raw_sha256);
ALTER TABLE archive_entries ADD CONSTRAINT archive_raw_sha CHECK(raw_sha256='' OR raw_sha256 ~ '^[a-f0-9]{64}$');
CREATE TABLE archive_content (
 entry_id bigint PRIMARY KEY REFERENCES archive_entries(id) ON DELETE RESTRICT,
 tenant_id bigint NOT NULL REFERENCES tenants(id),
 original_sha256 text NOT NULL DEFAULT '' CHECK(original_sha256='' OR original_sha256 ~ '^[a-f0-9]{64}$'),
 body_html text NOT NULL DEFAULT '',
 retention_preview uuid,
 body_text text NOT NULL DEFAULT '',
 attachments jsonb NOT NULL DEFAULT '[]',
 extraction_error text NOT NULL DEFAULT '',
 purge_started_at timestamptz,
 purged_at timestamptz,
 purge_error text NOT NULL DEFAULT '',
 CHECK (purged_at IS NULL OR purge_started_at IS NOT NULL)
);
CREATE INDEX archive_content_search ON archive_content USING gin(to_tsvector('simple',body_text));
ALTER TABLE archive_content OWNER TO urmail_platform;
ALTER TABLE archive_content ENABLE ROW LEVEL SECURITY;
ALTER TABLE archive_content FORCE ROW LEVEL SECURITY;
CREATE POLICY archive_content_tenant ON archive_content FOR SELECT USING(
 tenant_id = nullif(current_setting('urmail.tenant_id',true),'')::bigint
 AND EXISTS(SELECT 1 FROM compliance_grants g JOIN user_tenant_roles r ON r.tenant_id=g.tenant_id AND r.user_id=g.user_id
 WHERE g.tenant_id=archive_content.tenant_id AND g.user_id=nullif(current_setting('urmail.user_id',true),'')::bigint
 AND g.scope IN('full_content','export') AND NOT g.revoked AND g.owner_confirmed_at IS NOT NULL AND g.expires_at>statement_timestamp()
 AND g.granted_by IS NOT NULL AND length(btrim(g.reason))>0 AND r.role IN('owner','admin')));
REVOKE ALL ON archive_content FROM PUBLIC,urmail_app,urmail_dovecot;
GRANT SELECT ON archive_content TO urmail_app;
CREATE TABLE retention_previews (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id bigint NOT NULL REFERENCES tenants(id),
 retention_days integer NOT NULL CHECK(retention_days>0),
 candidate_count bigint NOT NULL, candidate_digest text NOT NULL,
 created_by bigint NOT NULL REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '15 minutes',
 confirmed_at timestamptz, UNIQUE(tenant_id,id)
);
ALTER TABLE archive_content ADD FOREIGN KEY(retention_preview) REFERENCES retention_previews(id);
ALTER TABLE retention_previews OWNER TO urmail_platform;
ALTER TABLE retention_previews ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_previews FORCE ROW LEVEL SECURITY;
CREATE POLICY retention_preview_tenant ON retention_previews USING(tenant_id = nullif(current_setting('urmail.tenant_id',true),'')::bigint) WITH CHECK(tenant_id = nullif(current_setting('urmail.tenant_id',true),'')::bigint);
REVOKE ALL ON retention_previews FROM PUBLIC,urmail_app,urmail_dovecot;
GRANT SELECT,INSERT,UPDATE ON retention_previews TO urmail_app;
-- Existing active policies lack an explicit preview confirmation and are disabled.
ALTER TABLE retention_policies DISABLE TRIGGER retention_policies_content_admin_guard;
UPDATE retention_policies SET active=false;
ALTER TABLE retention_policies ENABLE TRIGGER retention_policies_content_admin_guard;
ALTER TABLE retention_policies ALTER COLUMN active SET DEFAULT false;
ALTER TABLE retention_policies ADD COLUMN confirmed_preview uuid REFERENCES retention_previews(id);
CREATE FUNCTION urmail_archive_purge_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF TG_TABLE_NAME='archive_entries' THEN
  IF NEW.legal_hold AND EXISTS(SELECT 1 FROM archive_content WHERE entry_id=NEW.id AND (purge_started_at IS NOT NULL OR purged_at IS NOT NULL)) THEN RAISE EXCEPTION 'ARCHIVE_PURGE_STARTED' USING ERRCODE='23514'; END IF;
 ELSE
  IF NEW.purge_started_at IS NOT NULL AND (OLD.purge_started_at IS NULL OR NEW.purged_at IS DISTINCT FROM OLD.purged_at) AND NOT EXISTS(SELECT 1 FROM archive_entries WHERE id=NEW.entry_id AND tenant_id=NEW.tenant_id AND NOT legal_hold AND deleted_at<=statement_timestamp()-interval '30 days') THEN RAISE EXCEPTION 'ARCHIVE_PURGE_INELIGIBLE' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_archive_purge_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_archive_purge_guard() FROM PUBLIC;
CREATE TRIGGER archive_physical_hold_guard BEFORE UPDATE OF legal_hold ON archive_entries FOR EACH ROW EXECUTE FUNCTION urmail_archive_purge_guard();
CREATE TRIGGER archive_physical_purge_guard BEFORE UPDATE ON archive_content FOR EACH ROW EXECUTE FUNCTION urmail_archive_purge_guard();
CREATE FUNCTION urmail_retention_confirmation_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.active AND NOT EXISTS(SELECT 1 FROM retention_previews p WHERE p.id=NEW.confirmed_preview AND p.tenant_id=NEW.tenant_id AND p.retention_days=NEW.retention_days AND p.confirmed_at IS NOT NULL) THEN RAISE EXCEPTION 'RETENTION_PREVIEW_REQUIRED' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_retention_confirmation_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_retention_confirmation_guard() FROM PUBLIC;
CREATE TRIGGER retention_confirmation_guard BEFORE INSERT OR UPDATE ON retention_policies FOR EACH ROW EXECUTE FUNCTION urmail_retention_confirmation_guard();

-- Manual deletion has its own explicit intent, independent from policy revocation.
CREATE FUNCTION urmail_soft_delete_archive(p_tenant bigint,p_id bigint) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE selected bigint;
BEGIN
 IF p_tenant IS DISTINCT FROM nullif(current_setting('urmail.tenant_id',true),'')::bigint
 OR NOT EXISTS(SELECT 1 FROM user_tenant_roles WHERE tenant_id=p_tenant AND user_id=nullif(current_setting('urmail.user_id',true),'')::bigint AND role IN('owner','admin'))
 THEN RAISE EXCEPTION 'ARCHIVE_ROLE_REQUIRED' USING ERRCODE='42501'; END IF;
 SELECT a.id INTO selected FROM archive_entries a LEFT JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
 WHERE a.tenant_id=p_tenant AND a.id=p_id AND NOT a.legal_hold AND a.deleted_at IS NULL AND c.purge_started_at IS NULL FOR UPDATE OF a;
 IF selected IS NULL THEN RETURN NULL; END IF;
 UPDATE archive_entries SET deleted_at=statement_timestamp() WHERE tenant_id=p_tenant AND id=selected;
 UPDATE archive_content SET retention_preview=NULL WHERE tenant_id=p_tenant AND entry_id=selected;
 RETURN selected;
END $$;
ALTER FUNCTION urmail_soft_delete_archive(bigint,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_soft_delete_archive(bigint,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_soft_delete_archive(bigint,bigint) TO urmail_app;

DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_extension WHERE extname='pg_trgm') THEN
  EXECUTE 'CREATE INDEX archive_content_body_trgm ON archive_content USING gin(body_text gin_trgm_ops)';
 END IF;
END $$;
