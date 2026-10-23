-- Wildcards are valid SMTP characters but unsafe as exact doveadm targets.
DO $$ DECLARE bad text; BEGIN
 SELECT string_agg(id::text,',' ORDER BY id) INTO bad FROM mailboxes WHERE local_part ~ '[*?]';
 IF bad IS NOT NULL THEN RAISE EXCEPTION 'MAILBOX_WILDCARD_PREFLIGHT: review mailbox IDs %; no rows changed',bad; END IF;
END $$;
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_no_command_wildcard CHECK(local_part !~ '[*?]');
-- A physical address must never attach an old Maildir to a new mailbox ID.
-- Reservations deliberately survive mailbox/tenant deletion and have no FK.
CREATE TABLE mailbox_storage_reservations(address text PRIMARY KEY,mailbox_id bigint NOT NULL UNIQUE);
INSERT INTO mailbox_storage_reservations SELECT address,id FROM mailboxes;
ALTER TABLE mailbox_storage_reservations OWNER TO urmail_platform;
ALTER TABLE mailbox_storage_reservations ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_storage_reservations FORCE ROW LEVEL SECURITY;
REVOKE ALL ON mailbox_storage_reservations FROM PUBLIC,urmail_app,urmail_dovecot;
CREATE FUNCTION urmail_reserve_mailbox_storage() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$ BEGIN
 IF TG_OP='UPDATE' AND (NEW.address<>OLD.address OR NEW.local_part<>OLD.local_part OR NEW.domain_id<>OLD.domain_id OR NEW.tenant_id<>OLD.tenant_id) THEN RAISE EXCEPTION 'MAILBOX_STORAGE_IDENTITY_IMMUTABLE' USING ERRCODE='23514'; END IF;
 INSERT INTO mailbox_storage_reservations(address,mailbox_id) VALUES(NEW.address,NEW.id) ON CONFLICT DO NOTHING;
 IF NOT EXISTS(SELECT 1 FROM mailbox_storage_reservations WHERE address=NEW.address AND mailbox_id=NEW.id) THEN RAISE EXCEPTION 'MAILBOX_STORAGE_ADDRESS_RESERVED' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_reserve_mailbox_storage() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_reserve_mailbox_storage() FROM PUBLIC;
CREATE TRIGGER reserve_mailbox_storage BEFORE INSERT OR UPDATE OF address,local_part,domain_id,tenant_id ON mailboxes FOR EACH ROW EXECUTE FUNCTION urmail_reserve_mailbox_storage();
