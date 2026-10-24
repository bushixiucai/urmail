-- A durable version lets the agent retry credential disconnects after restart.
ALTER TABLE mailboxes ADD COLUMN password_version bigint NOT NULL DEFAULT 0 CHECK(password_version>=0),
 ADD COLUMN password_revoke_pending boolean NOT NULL DEFAULT false;
CREATE FUNCTION urmail_mailbox_password_version_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE trusted boolean;
BEGIN
 trusted:=current_setting('role',true) IN('urmail_platform','urmail_migrate','urmail_dovecot')
  OR (current_setting('role',true)='none' AND (pg_has_role(session_user,'urmail_platform','USAGE') OR pg_has_role(session_user,'urmail_dovecot','USAGE')));
 IF TG_OP='INSERT' THEN
  IF NOT trusted AND (NEW.password_version<>0 OR NEW.password_revoke_pending) THEN
   RAISE EXCEPTION 'MAILBOX_PASSWORD_VERSION_IMMUTABLE' USING ERRCODE='42501';
  END IF;
  RETURN NEW;
 END IF;
 IF NEW.password_hash IS DISTINCT FROM OLD.password_hash THEN
  IF OLD.password_version=9223372036854775807 THEN RAISE EXCEPTION 'MAILBOX_PASSWORD_VERSION_EXHAUSTED' USING ERRCODE='23514'; END IF;
  NEW.password_version:=OLD.password_version+1;
  NEW.password_revoke_pending:=true;
 ELSE
  IF NEW.password_version<>OLD.password_version OR (NEW.password_revoke_pending IS DISTINCT FROM OLD.password_revoke_pending AND NOT trusted) THEN
   RAISE EXCEPTION 'MAILBOX_PASSWORD_VERSION_IMMUTABLE' USING ERRCODE='42501';
  END IF;
 END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_mailbox_password_version_guard() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_mailbox_password_version_guard() FROM PUBLIC;
CREATE TRIGGER mailbox_password_version_guard BEFORE INSERT OR UPDATE ON mailboxes
 FOR EACH ROW EXECUTE FUNCTION urmail_mailbox_password_version_guard();
