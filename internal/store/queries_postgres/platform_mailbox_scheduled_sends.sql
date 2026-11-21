-- Service-only schedule metadata; message bodies stay in IMAP.

-- name: PlatformCreateMailboxScheduledSend :one
INSERT INTO mailbox_scheduled_sends(
 tenant_id,mailbox_id,draft_folder,draft_uid,draft_uidvalidity,send_at,next_attempt_at,
 status,smtp_secret,idempotency_key,request_hash,smtp_password_version
) VALUES(
 sqlc.arg(tenant_id),sqlc.arg(mailbox_id),sqlc.arg(draft_folder),sqlc.arg(draft_uid),
 sqlc.arg(draft_uidvalidity),sqlc.arg(send_at),sqlc.arg(send_at),'pending',
 sqlc.arg(smtp_secret),sqlc.arg(idempotency_key),sqlc.arg(request_hash),(SELECT md5(m.password_hash) FROM mailboxes m WHERE m.tenant_id=sqlc.arg(tenant_id) AND m.id=sqlc.arg(mailbox_id))
) RETURNING *;

-- name: PlatformGetMailboxScheduledSendByKey :one
SELECT * FROM mailbox_scheduled_sends
WHERE tenant_id=$1 AND mailbox_id=$2 AND idempotency_key=$3;

-- name: PlatformGetMailboxScheduledSend :one
SELECT * FROM mailbox_scheduled_sends
WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3;

-- name: PlatformListMailboxScheduledSends :many
SELECT * FROM mailbox_scheduled_sends
WHERE tenant_id=$1 AND mailbox_id=$2
ORDER BY created_at DESC,id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: PlatformCancelMailboxScheduledSend :one
UPDATE mailbox_scheduled_sends
SET status='cancelled',smtp_secret='',sent_raw_secret='',sent_raw=NULL,claim_token=claim_token+1,lease_until=NULL,
 updated_at=now(),last_error=NULL
WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND status IN('pending','preparing','cancelled')
RETURNING *;

-- name: PlatformClaimMailboxScheduledSend :one
WITH candidate AS (
 SELECT s.id FROM mailbox_scheduled_sends s
 JOIN mailboxes m ON m.tenant_id=s.tenant_id AND m.id=s.mailbox_id
 JOIN tenants t ON t.id=s.tenant_id
 JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
 WHERE s.status='pending' AND s.next_attempt_at<=now() AND s.draft_uidvalidity>0
  AND m.status='active' AND t.status='active' AND d.status='active'
 ORDER BY s.next_attempt_at ASC,s.id ASC
 FOR UPDATE OF s SKIP LOCKED LIMIT 1
)
UPDATE mailbox_scheduled_sends s
SET status='preparing',attempts=s.attempts+1,claim_token=s.claim_token+1,
 lease_until=now()+interval '5 minutes',updated_at=now()
FROM candidate c WHERE s.id=c.id
RETURNING s.*;

-- name: PlatformGetMailboxScheduledSendAddress :one
SELECT m.address FROM mailboxes m
JOIN tenants t ON t.id=m.tenant_id
JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE m.tenant_id=sqlc.arg(tenant_id) AND m.id=sqlc.arg(mailbox_id)
 AND m.status='active' AND t.status='active' AND d.status='active';

-- name: PlatformBeginMailboxScheduledSendSubmission :one
UPDATE mailbox_scheduled_sends s SET status='submitting',updated_at=now()
WHERE s.id=sqlc.arg(id) AND s.claim_token=sqlc.arg(claim_token)
 AND s.status='preparing' AND s.lease_until>now()
 AND EXISTS(SELECT 1 FROM mailboxes m JOIN tenants t ON t.id=m.tenant_id
  JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
  WHERE m.tenant_id=s.tenant_id AND m.id=s.mailbox_id
   AND m.status='active' AND t.status='active' AND d.status='active')
RETURNING s.*;

-- name: PlatformFinishMailboxScheduledSend :one
UPDATE mailbox_scheduled_sends
SET status=sqlc.arg(final_status),smtp_secret='',sent_raw_secret=CASE WHEN sqlc.arg(final_status)::text='failed' THEN '' ELSE sent_raw_secret END,last_error=sqlc.narg(last_error),
 lease_until=NULL,updated_at=now(),sent_at=CASE WHEN sqlc.arg(final_status)::text='sent' THEN now() ELSE sent_at END
WHERE id=sqlc.arg(id) AND claim_token=sqlc.arg(claim_token) AND status=sqlc.arg(expected_status)
RETURNING *;

-- name: PlatformRequeueMailboxScheduledSend :one
UPDATE mailbox_scheduled_sends
SET status='pending',next_attempt_at=sqlc.arg(next_attempt_at),last_error=sqlc.narg(last_error),
 lease_until=NULL,updated_at=now()
WHERE id=sqlc.arg(id) AND claim_token=sqlc.arg(claim_token) AND status=sqlc.arg(expected_status)
RETURNING *;

-- name: PlatformRecoverStaleMailboxScheduledSends :execrows
UPDATE mailbox_scheduled_sends
SET status=CASE WHEN status='preparing' THEN 'pending' ELSE 'uncertain' END,
 next_attempt_at=now()+interval '30 seconds',claim_token=claim_token+1,
 smtp_secret=CASE WHEN status='preparing' THEN smtp_secret ELSE '' END,
 lease_until=NULL,updated_at=now(),
 last_error=CASE WHEN status='preparing' THEN 'SCHEDULE_PREPARATION_EXPIRED' ELSE 'SCHEDULE_DELIVERY_UNCERTAIN' END
WHERE status IN('preparing','submitting') AND lease_until<now();

-- name: PlatformAcknowledgeUncertainScheduledSend :one
UPDATE mailbox_scheduled_sends SET status='cancelled',smtp_secret='',sent_raw=NULL,sent_raw_secret='',lease_until=NULL,claim_token=claim_token+1,last_error='SCHEDULE_UNCERTAIN_ACKNOWLEDGED',updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND status IN('uncertain','held') RETURNING *;
