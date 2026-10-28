-- name: PlatformAcceptMailboxScheduledSend :one
UPDATE mailbox_scheduled_sends
SET status='sent',sent_at=now(),sent_raw=sqlc.arg(sent_raw),sent_copy_state='pending',
 last_error='SENT_COPY_PENDING',updated_at=now(),lease_until=now()+interval '2 minutes'
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id)
 AND id=sqlc.arg(id) AND claim_token=sqlc.arg(claim_token) AND status='submitting'
RETURNING *;

-- name: PlatformClaimScheduledSentCopy :one
WITH candidate AS (
 SELECT id FROM mailbox_scheduled_sends
 WHERE NOT urmail_restore_quarantine_active() AND status='sent' AND sent_raw IS NOT NULL AND sent_copy_state IN('pending','unknown')
 AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now())
 ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE mailbox_scheduled_sends s SET claim_token=claim_token+1,lease_until=now()+interval '2 minutes'
FROM candidate c WHERE s.id=c.id RETURNING s.*;

-- name: PlatformClaimOwnedScheduledSentCopy :one
UPDATE mailbox_scheduled_sends SET claim_token=claim_token+1,lease_until=now()+interval '2 minutes'
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND status='sent' AND sent_raw IS NOT NULL AND sent_copy_state IN('pending','unknown','held')
 AND (lease_until IS NULL OR lease_until<now()) RETURNING *;

-- name: PlatformMarkScheduledSentCopyUnknown :one
UPDATE mailbox_scheduled_sends SET sent_copy_state='unknown',updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND claim_token=sqlc.arg(claim_token) AND status='sent' AND sent_raw IS NOT NULL
 AND lease_until>now() RETURNING *;

-- name: PlatformCompleteScheduledSentCopy :one
UPDATE mailbox_scheduled_sends SET sent_copy_state='complete',sent_raw=NULL,smtp_secret='',
 last_error=NULL,lease_until=NULL,updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND claim_token=sqlc.arg(claim_token) AND status='sent' AND sent_raw IS NOT NULL RETURNING *;

-- name: PlatformDeferScheduledSentCopy :exec
UPDATE mailbox_scheduled_sends SET lease_until=NULL,next_attempt_at=now()+interval '5 minutes',
 last_error='SENT_COPY_PENDING',updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND claim_token=sqlc.arg(claim_token) AND status='sent' AND sent_raw IS NOT NULL;
