DROP TRIGGER archive_hold_guard ON archive_entries;
DROP TRIGGER mailbox_hold_guard ON mailboxes;
DROP TRIGGER domain_hold_guard ON domains;
DROP TRIGGER tenant_hold_guard ON tenants;
DROP FUNCTION urmail_hold_guard();
