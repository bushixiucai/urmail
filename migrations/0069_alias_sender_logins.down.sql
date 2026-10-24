DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM mailbox_aliases WHERE cardinality(sender_logins)>0) THEN
  RAISE EXCEPTION 'ALIAS_SENDER_LOGIN_DOWNGRADE_REQUIRES_REVIEW';
 END IF;
END $$;
DROP TRIGGER alias_sender_logins_guard ON mailbox_aliases;
DROP FUNCTION urmail_alias_sender_logins_guard();
ALTER TABLE mailbox_aliases DROP COLUMN sender_logins;
