-- Refuse to discard unresolved accepted payloads during downgrade.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM mailbox_scheduled_sends WHERE sent_raw IS NOT NULL) THEN
  RAISE EXCEPTION 'Resolve pending scheduled Sent copies before downgrade';
 END IF;
END $$;
DROP INDEX mailbox_scheduled_sent_copy_due_idx;
ALTER TABLE mailbox_scheduled_sends DROP CONSTRAINT scheduled_sent_raw_size;
ALTER TABLE mailbox_scheduled_sends DROP COLUMN sent_copy_state, DROP COLUMN sent_raw;
ALTER TABLE mailbox_scheduled_sends NO FORCE ROW LEVEL SECURITY;
