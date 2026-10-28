-- Temporary recovery payloads are service-only and removed after Sent is verified.
ALTER TABLE mailbox_scheduled_sends
 ADD COLUMN sent_raw bytea,
 ADD COLUMN sent_copy_state text NOT NULL DEFAULT 'none'
 CHECK(sent_copy_state IN('none','pending','unknown','complete','held'));
ALTER TABLE mailbox_scheduled_sends ADD CONSTRAINT scheduled_sent_raw_size
 CHECK(sent_raw IS NULL OR octet_length(sent_raw) BETWEEN 1 AND 33554432);
ALTER TABLE mailbox_scheduled_sends FORCE ROW LEVEL SECURITY;
CREATE INDEX mailbox_scheduled_sent_copy_due_idx ON mailbox_scheduled_sends(next_attempt_at,id)
 WHERE status='sent' AND sent_copy_state IN('pending','unknown');
