-- Keep legacy schedule rows as evidence; their IMAP identity is incomplete.
ALTER TABLE mailbox_scheduled_sends
 ADD COLUMN draft_uidvalidity bigint NOT NULL DEFAULT 0 CHECK(draft_uidvalidity BETWEEN 0 AND 4294967295),
 ADD COLUMN idempotency_key text NOT NULL DEFAULT '',
 ADD COLUMN request_hash text NOT NULL DEFAULT '',
 ADD COLUMN claim_token bigint NOT NULL DEFAULT 0,
 ADD COLUMN lease_until timestamptz,
 ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now(),
 ADD COLUMN sent_at timestamptz;
ALTER TABLE mailbox_scheduled_sends DROP CONSTRAINT mailbox_scheduled_sends_status_check;
UPDATE mailbox_scheduled_sends SET
 status=CASE WHEN status IN('pending','sending') THEN 'held' ELSE status END,
 smtp_secret='',next_attempt_at=send_at,
 last_error=CASE WHEN status IN('pending','sending') THEN 'SCHEDULE_REVIEW_REQUIRED' ELSE last_error END;
ALTER TABLE mailbox_scheduled_sends ADD CONSTRAINT mailbox_scheduled_sends_status_check
 CHECK(status IN('pending','preparing','submitting','sent','uncertain','failed','cancelled','held'));
CREATE UNIQUE INDEX mailbox_scheduled_sends_idempotency_idx
 ON mailbox_scheduled_sends(tenant_id,mailbox_id,idempotency_key) WHERE idempotency_key<>'';
CREATE UNIQUE INDEX mailbox_scheduled_sends_active_draft_idx
 ON mailbox_scheduled_sends(tenant_id,mailbox_id,draft_folder,draft_uidvalidity,draft_uid)
 WHERE draft_uidvalidity>0 AND status IN('pending','preparing','submitting','uncertain','held');
DROP INDEX mailbox_scheduled_sends_due_idx;
CREATE INDEX mailbox_scheduled_sends_due_idx
 ON mailbox_scheduled_sends(next_attempt_at,id) WHERE status='pending';
