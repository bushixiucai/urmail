-- Plain DROP keeps the rollback working whether or not a later VALIDATE step ran.
ALTER TABLE mailbox_aliases DROP CONSTRAINT IF EXISTS alias_target_email_syntax;
