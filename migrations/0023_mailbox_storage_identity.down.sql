DROP TRIGGER mailbox_storage_identity ON mailboxes;
DROP FUNCTION urmail_mailbox_identity_guard();
ALTER TABLE mailboxes DROP CONSTRAINT mailboxes_storage_local_check;
ALTER TABLE domains DROP CONSTRAINT domains_storage_fqdn_check;
