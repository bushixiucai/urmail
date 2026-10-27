-- Extend restore quarantine to domain and mailbox creation.

CREATE TRIGGER domain_restore_quarantine_guard BEFORE INSERT ON domains
FOR EACH ROW EXECUTE FUNCTION urmail_restore_quarantine_guard();

CREATE TRIGGER mailbox_restore_quarantine_guard BEFORE INSERT ON mailboxes
FOR EACH ROW EXECUTE FUNCTION urmail_restore_quarantine_guard();
