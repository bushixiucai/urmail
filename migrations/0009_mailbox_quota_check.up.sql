-- Prevent negative mailbox quotas from reaching Dovecot (which would be
-- interpreted as an invalid quota rule).
ALTER TABLE mailboxes
    ADD CONSTRAINT mailboxes_quota_bytes_check CHECK (quota_bytes >= 0);
