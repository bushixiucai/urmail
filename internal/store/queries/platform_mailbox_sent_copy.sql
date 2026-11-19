-- name: PlatformAcceptMailboxScheduledSend :one
-- scope: platform

UPDATE mailbox_scheduled_sends
SET status='sent',sent_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),sent_raw=NULL,sent_raw_secret=convert_from(sqlc.arg(sent_raw)::bytea,'UTF8'),sent_copy_state='pending',copy_pending_since=COALESCE(copy_pending_since,(CAST(strftime('%s','now') AS INTEGER)*1000000)),
 last_error='SENT_COPY_PENDING',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),lease_until=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id)
 AND id=sqlc.arg(id) AND claim_token=sqlc.arg(claim_token) AND status='submitting'
RETURNING *;

-- name: PlatformClaimScheduledSentCopy :one
-- scope: platform

WITH candidate AS (
 SELECT id FROM mailbox_scheduled_sends
 WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND status='sent' AND (sent_raw IS NOT NULL OR sent_raw_secret<>'') AND sent_copy_state IN('pending','unknown')
 AND copy_pending_since>(CAST(strftime('%s','now') AS INTEGER)*1000000)-604800000000 AND next_attempt_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000) AND (lease_until IS NULL OR lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000))
 ORDER BY next_attempt_at,id LIMIT 1
)
UPDATE mailbox_scheduled_sends SET claim_token=claim_token+1,lease_until=(CAST(strftime('%s','now') AS INTEGER)*1000000),copy_attempts=copy_attempts+1
WHERE id=(SELECT id FROM candidate) AND (lease_until IS NULL OR lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000))
RETURNING *;

-- name: PlatformClaimOwnedScheduledSentCopy :one
-- scope: platform

UPDATE mailbox_scheduled_sends SET claim_token=claim_token+1,lease_until=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND status='sent' AND (sent_raw IS NOT NULL OR sent_raw_secret<>'') AND sent_copy_state IN('pending','unknown','held')
 AND (lease_until IS NULL OR lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000)) RETURNING *;

-- name: PlatformMarkScheduledSentCopyUnknown :one
-- scope: platform

UPDATE mailbox_scheduled_sends SET sent_copy_state='unknown',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND claim_token=sqlc.arg(claim_token) AND status='sent' AND (sent_raw IS NOT NULL OR sent_raw_secret<>'')
 AND lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) RETURNING *;

-- name: PlatformCompleteScheduledSentCopy :one
-- scope: platform

UPDATE mailbox_scheduled_sends SET sent_copy_state='complete',sent_raw=NULL,sent_raw_secret='',smtp_secret='',
 last_error=NULL,lease_until=NULL,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND claim_token=sqlc.arg(claim_token) AND lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND status='sent' AND (sent_raw IS NOT NULL OR sent_raw_secret<>'') RETURNING *;

-- name: PlatformDeferScheduledSentCopy :exec
-- scope: platform

UPDATE mailbox_scheduled_sends SET lease_until=NULL,next_attempt_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)+(min(3600*1000000)))),sent_copy_state=CASE WHEN copy_pending_since<=(CAST(strftime('%s','now') AS INTEGER)*1000000)-604800000000 OR smtp_secret='' THEN 'held' ELSE sent_copy_state END,smtp_secret=CASE WHEN copy_pending_since<=(CAST(strftime('%s','now') AS INTEGER)*1000000)-604800000000 THEN '' ELSE smtp_secret END,
 last_error='SENT_COPY_PENDING',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND claim_token=sqlc.arg(claim_token) AND status='sent' AND (sent_raw IS NOT NULL OR sent_raw_secret<>'');

-- name: PlatformHoldExpiredScheduledCopies :execrows
-- scope: platform

UPDATE mailbox_scheduled_sends SET sent_copy_state='held',smtp_secret='',lease_until=NULL,claim_token=claim_token+1,last_error='SENT_COPY_PENDING',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND status='sent' AND sent_copy_state IN('pending','unknown') AND copy_pending_since<=(CAST(strftime('%s','now') AS INTEGER)*1000000)-604800000000 AND (lease_until IS NULL OR lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000));

-- name: PlatformFreezeScheduledSend :one
-- scope: platform

UPDATE mailbox_scheduled_sends SET sent_raw_secret=sqlc.arg(raw_secret),sent_raw=NULL,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND id=sqlc.arg(id) AND claim_token=sqlc.arg(claim_token) AND status='preparing' AND lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) RETURNING *;

-- name: PlatformHoldUnauthorizedScheduledCopies :execrows
-- scope: platform

UPDATE mailbox_scheduled_sends SET sent_copy_state='held',smtp_secret='',lease_until=NULL,claim_token=claim_token+1,last_error='SENT_COPY_REAUTH_REQUIRED',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND status='sent' AND sent_copy_state IN('pending','unknown')
AND NOT EXISTS(SELECT 1 FROM mailboxes m JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE m.tenant_id=tenant_id AND m.id=mailbox_id AND m.status='active' AND t.status='active' AND d.status='active' AND md5(m.password_hash)=smtp_password_version);

-- name: PlatformGetLegacyScheduledCopy :one
-- scope: platform

SELECT * FROM mailbox_scheduled_sends WHERE sent_raw IS NOT NULL ORDER BY id LIMIT 1;

-- name: PlatformSealLegacyScheduledCopy :exec
-- scope: platform

UPDATE mailbox_scheduled_sends SET sent_raw_secret=?1,sent_raw=NULL,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE tenant_id=?2 AND mailbox_id=?3 AND id=?4;

-- name: PlatformAuthorizeScheduledCopy :one
-- scope: platform

SELECT r.id FROM mailbox_scheduled_sends r
JOIN mailboxes m ON m.tenant_id=r.tenant_id AND m.id=r.mailbox_id
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE r.tenant_id=?1 AND r.mailbox_id=?2 AND r.id=?3 AND r.claim_token=?4 AND r.status='sent' AND r.lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000)
AND NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND m.status='active' AND t.status='active' AND d.status='active' AND md5(m.password_hash)=r.smtp_password_version;
