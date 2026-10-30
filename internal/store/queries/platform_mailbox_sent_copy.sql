-- name: PlatformAcceptMailboxScheduledSend :one
UPDATE mailbox_scheduled_sends
SET status='sent',sent_at=now(),sent_raw=NULL,sent_raw_secret=convert_from(sqlc.arg(sent_raw)::bytea,'UTF8'),sent_copy_state='pending',copy_pending_since=COALESCE(copy_pending_since,now()),
 last_error='SENT_COPY_PENDING',updated_at=now(),lease_until=now()+interval '2 minutes'
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id)
 AND id=sqlc.arg(id) AND claim_token=sqlc.arg(claim_token) AND status='submitting'
RETURNING *;

-- name: PlatformClaimScheduledSentCopy :one
WITH candidate AS (
 SELECT id FROM mailbox_scheduled_sends
 WHERE NOT urmail_restore_quarantine_active() AND status='sent' AND (sent_raw IS NOT NULL OR sent_raw_secret<>'') AND sent_copy_state IN('pending','unknown')
 AND copy_pending_since>now()-interval '7 days' AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now())
 ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE mailbox_scheduled_sends s SET claim_token=claim_token+1,lease_until=now()+interval '2 minutes',copy_attempts=copy_attempts+1
FROM candidate c WHERE s.id=c.id RETURNING s.*;

-- name: PlatformClaimOwnedScheduledSentCopy :one
UPDATE mailbox_scheduled_sends SET claim_token=claim_token+1,lease_until=now()+interval '2 minutes'
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND status='sent' AND (sent_raw IS NOT NULL OR sent_raw_secret<>'') AND sent_copy_state IN('pending','unknown','held')
 AND (lease_until IS NULL OR lease_until<now()) RETURNING *;

-- name: PlatformMarkScheduledSentCopyUnknown :one
UPDATE mailbox_scheduled_sends SET sent_copy_state='unknown',updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND claim_token=sqlc.arg(claim_token) AND status='sent' AND (sent_raw IS NOT NULL OR sent_raw_secret<>'')
 AND lease_until>now() RETURNING *;

-- name: PlatformCompleteScheduledSentCopy :one
UPDATE mailbox_scheduled_sends SET sent_copy_state='complete',sent_raw=NULL,sent_raw_secret='',smtp_secret='',
 last_error=NULL,lease_until=NULL,updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND claim_token=sqlc.arg(claim_token) AND lease_until>now() AND status='sent' AND (sent_raw IS NOT NULL OR sent_raw_secret<>'') RETURNING *;

-- name: PlatformDeferScheduledSentCopy :exec
UPDATE mailbox_scheduled_sends SET lease_until=NULL,next_attempt_at=now()+make_interval(secs=>LEAST(3600,30*power(2,LEAST(copy_attempts,7)))::int),sent_copy_state=CASE WHEN copy_pending_since<=now()-interval '7 days' OR smtp_secret='' THEN 'held' ELSE sent_copy_state END,smtp_secret=CASE WHEN copy_pending_since<=now()-interval '7 days' THEN '' ELSE smtp_secret END,
 last_error='SENT_COPY_PENDING',updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND claim_token=sqlc.arg(claim_token) AND status='sent' AND (sent_raw IS NOT NULL OR sent_raw_secret<>'');

-- name: PlatformHoldExpiredScheduledCopies :execrows
UPDATE mailbox_scheduled_sends SET sent_copy_state='held',smtp_secret='',lease_until=NULL,claim_token=claim_token+1,last_error='SENT_COPY_PENDING',updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND status='sent' AND sent_copy_state IN('pending','unknown') AND copy_pending_since<=now()-interval '7 days' AND (lease_until IS NULL OR lease_until<now());

-- name: PlatformFreezeScheduledSend :one
UPDATE mailbox_scheduled_sends SET sent_raw_secret=sqlc.arg(raw_secret),sent_raw=NULL,updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND id=sqlc.arg(id) AND claim_token=sqlc.arg(claim_token) AND status='preparing' AND lease_until>now() RETURNING *;

-- name: PlatformHoldUnauthorizedScheduledCopies :execrows
UPDATE mailbox_scheduled_sends r SET sent_copy_state='held',smtp_secret='',lease_until=NULL,claim_token=claim_token+1,last_error='SENT_COPY_REAUTH_REQUIRED',updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND r.status='sent' AND r.sent_copy_state IN('pending','unknown')
AND NOT EXISTS(SELECT 1 FROM mailboxes m JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE m.tenant_id=r.tenant_id AND m.id=r.mailbox_id AND m.status='active' AND t.status='active' AND d.status='active' AND md5(m.password_hash)=r.smtp_password_version);

-- name: PlatformGetLegacyScheduledCopy :one
SELECT * FROM mailbox_scheduled_sends WHERE sent_raw IS NOT NULL ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1;

-- name: PlatformSealLegacyScheduledCopy :exec
UPDATE mailbox_scheduled_sends SET sent_raw_secret=$1,sent_raw=NULL,updated_at=now() WHERE tenant_id=$2 AND mailbox_id=$3 AND id=$4;

-- name: PlatformAuthorizeScheduledCopy :one
SELECT r.id FROM mailbox_scheduled_sends r
JOIN mailboxes m ON m.tenant_id=r.tenant_id AND m.id=r.mailbox_id
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE r.tenant_id=$1 AND r.mailbox_id=$2 AND r.id=$3 AND r.claim_token=$4 AND r.status='sent' AND r.lease_until>now()
AND NOT urmail_restore_quarantine_active() AND m.status='active' AND t.status='active' AND d.status='active' AND md5(m.password_hash)=r.smtp_password_version;
