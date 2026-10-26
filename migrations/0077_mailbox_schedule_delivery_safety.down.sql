-- A downgrade must never turn delivery uncertainty back into runnable work.
DROP INDEX mailbox_scheduled_sends_active_draft_idx;
DROP INDEX mailbox_scheduled_sends_idempotency_idx;
DROP INDEX mailbox_scheduled_sends_due_idx;
UPDATE mailbox_scheduled_sends SET status='failed',smtp_secret='',
 last_error='SCHEDULE_DOWNGRADE_REQUIRES_REVIEW'
 WHERE status IN('pending','preparing','submitting','uncertain','held');
ALTER TABLE mailbox_scheduled_sends DROP CONSTRAINT mailbox_scheduled_sends_status_check;
ALTER TABLE mailbox_scheduled_sends ADD CONSTRAINT mailbox_scheduled_sends_status_check
 CHECK(status IN('pending','sending','sent','failed','cancelled'));
ALTER TABLE mailbox_scheduled_sends DROP COLUMN draft_uidvalidity,
 DROP COLUMN idempotency_key,DROP COLUMN request_hash,DROP COLUMN claim_token,
 DROP COLUMN lease_until,DROP COLUMN next_attempt_at,DROP COLUMN sent_at;
CREATE INDEX mailbox_scheduled_sends_due_idx
 ON mailbox_scheduled_sends(send_at,id) WHERE status='pending';
