-- Maildir paths interpolate domain/local-part. RFC-valid slashes are unsafe
-- in this storage layout, even when the SMTP address itself is valid.
DO $$ DECLARE bad bigint; BEGIN
 SELECT count(*) INTO bad FROM mailboxes m JOIN domains d ON d.id=m.domain_id WHERE m.local_part IN('.','..') OR m.local_part ~ '[/\\]' OR m.address<>m.local_part||'@'||d.fqdn;
 IF bad>0 THEN RAISE EXCEPTION 'MAILBOX_IDENTITY_PREFLIGHT: % unsafe mailbox identities; review IDs with urmailctl data-check; no rows changed',bad; END IF;
END $$;
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_storage_local_check CHECK(length(local_part) BETWEEN 1 AND 64 AND local_part NOT IN('.','..') AND local_part !~ '[/\\]');
ALTER TABLE domains ADD CONSTRAINT domains_storage_fqdn_check CHECK(length(fqdn)<=253 AND fqdn=lower(fqdn) AND fqdn ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$');
CREATE FUNCTION urmail_mailbox_identity_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM domains d WHERE d.id=NEW.domain_id AND d.tenant_id=NEW.tenant_id AND NEW.address=NEW.local_part||'@'||d.fqdn) THEN RAISE EXCEPTION 'INVALID_MAILBOX_IDENTITY' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_mailbox_identity_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_mailbox_identity_guard() FROM PUBLIC;
CREATE TRIGGER mailbox_storage_identity BEFORE INSERT OR UPDATE OF address,domain_id,local_part,tenant_id ON mailboxes FOR EACH ROW EXECUTE FUNCTION urmail_mailbox_identity_guard();
