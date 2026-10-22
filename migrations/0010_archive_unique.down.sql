ALTER TABLE archive_entries
    DROP CONSTRAINT IF EXISTS archive_entries_tenant_mailbox_message_uniq;
ALTER TABLE archive_entries
    ADD CONSTRAINT archive_entries_tenant_id_message_id_key
    UNIQUE (tenant_id, message_id);
