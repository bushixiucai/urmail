DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM mailboxes WHERE password_revoke_pending OR password_version<>0) THEN
  RAISE EXCEPTION 'MAILBOX_PASSWORD_VERSIONS_REQUIRE_CONTROLLED_ROLLBACK' USING ERRCODE='23514';
 END IF;
END $$;
DROP TRIGGER mailbox_password_version_guard ON mailboxes;
DROP FUNCTION urmail_mailbox_password_version_guard();
ALTER TABLE mailboxes DROP COLUMN password_revoke_pending,DROP COLUMN password_version;
