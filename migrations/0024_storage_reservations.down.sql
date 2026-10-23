DROP TRIGGER reserve_mailbox_storage ON mailboxes;
DROP FUNCTION urmail_reserve_mailbox_storage();
DROP TABLE mailbox_storage_reservations;
ALTER TABLE mailboxes DROP CONSTRAINT mailboxes_no_command_wildcard;
