DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM mailboxes WHERE sieve_mode='legacy_v80') THEN
  RAISE EXCEPTION 'LEGACY_SIEVE_MAILBOXES_REQUIRE_CONTROLLED_ROLLBACK';
 END IF;
END $$;
DROP TRIGGER mailbox_sieve_mode_guard ON mailboxes;
DROP FUNCTION urmail_mailbox_sieve_mode_guard();
ALTER TABLE mailboxes DROP COLUMN sieve_mode;
