-- Archive one copy per mailbox, not one copy per tenant. A message delivered
-- to several mailboxes of the same tenant must keep one entry per mailbox.
ALTER TABLE archive_entries
    DROP CONSTRAINT IF EXISTS archive_entries_tenant_id_message_id_key;
ALTER TABLE archive_entries
    ADD CONSTRAINT archive_entries_tenant_mailbox_message_uniq
    UNIQUE (tenant_id, mailbox_id, message_id);
