-- name: PlatformCreateSendReceipt :one
INSERT INTO mailbox_send_receipts(tenant_id,mailbox_id,request_key,request_hash,message_id,raw_secret,smtp_secret,draft_folder,draft_uid,draft_uidvalidity,smtp_password_version,lease_until)
SELECT sqlc.arg(tenant_id),sqlc.arg(mailbox_id),sqlc.arg(request_key),sqlc.arg(request_hash),sqlc.arg(message_id),sqlc.arg(raw_secret),sqlc.arg(smtp_secret),sqlc.arg(draft_folder),sqlc.arg(draft_uid),sqlc.arg(draft_uidvalidity),(SELECT md5(m.password_hash) FROM mailboxes m WHERE m.tenant_id=sqlc.arg(tenant_id) AND m.id=sqlc.arg(mailbox_id)),now()+interval '2 minutes'
WHERE NOT urmail_restore_quarantine_active()
ON CONFLICT(tenant_id,mailbox_id,request_key) DO NOTHING RETURNING *;

-- name: PlatformGetSendReceipt :one
SELECT * FROM mailbox_send_receipts WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3;

-- name: PlatformGetSendReceiptByKey :one
SELECT * FROM mailbox_send_receipts WHERE tenant_id=$1 AND mailbox_id=$2 AND request_key=$3;

-- name: PlatformListSendReceipts :many
SELECT * FROM mailbox_send_receipts WHERE tenant_id=$1 AND mailbox_id=$2 ORDER BY id DESC LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: PlatformBeginSendReceipt :one
UPDATE mailbox_send_receipts r SET status='submitting',claim_token=claim_token+1,lease_until=now()+interval '2 minutes',updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND r.tenant_id=sqlc.arg(tenant_id) AND r.mailbox_id=sqlc.arg(mailbox_id) AND r.id=sqlc.arg(id) AND r.status='prepared' AND r.lease_until>now()
AND EXISTS(SELECT 1 FROM mailboxes m JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE m.tenant_id=r.tenant_id AND m.id=r.mailbox_id AND m.status='active' AND t.status='active' AND d.status='active') RETURNING r.*;

-- name: PlatformFinishSendReceipt :one
UPDATE mailbox_send_receipts SET status=sqlc.arg(final_status),last_error=sqlc.arg(last_error),
 accepted_at=CASE WHEN sqlc.arg(final_status)::text='accepted' THEN now() ELSE accepted_at END,
 sent_copy_state=CASE WHEN sqlc.arg(final_status)::text='accepted' THEN 'pending' ELSE 'none' END,
 copy_pending_since=CASE WHEN sqlc.arg(final_status)::text='accepted' THEN COALESCE(copy_pending_since,now()) ELSE copy_pending_since END,
 smtp_secret=CASE WHEN sqlc.arg(final_status)::text='accepted' THEN smtp_secret ELSE '' END,
 raw_secret=CASE WHEN sqlc.arg(final_status)::text='rejected' THEN '' ELSE raw_secret END,
 lease_until=NULL,updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id) AND claim_token=sqlc.arg(claim_token) AND status='submitting' RETURNING *;

-- name: PlatformRecoverSendReceipts :execrows
UPDATE mailbox_send_receipts SET status=CASE WHEN status='prepared' THEN 'rejected' ELSE 'uncertain' END,
 last_error=CASE WHEN status='prepared' THEN 'SEND_NOT_SUBMITTED' ELSE 'MAILBOX_SEND_UNCERTAIN' END,
 raw_secret=CASE WHEN status='prepared' THEN '' ELSE raw_secret END,smtp_secret='',claim_token=claim_token+1,lease_until=NULL,updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND status IN('prepared','submitting') AND lease_until<now();

-- name: PlatformHoldExpiredSendCopies :execrows
UPDATE mailbox_send_receipts SET sent_copy_state='held',smtp_secret='',last_error='SENT_COPY_PENDING',claim_token=claim_token+1,lease_until=NULL,updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND status='accepted' AND sent_copy_state IN('pending','unknown') AND copy_pending_since<=now()-interval '7 days' AND (lease_until IS NULL OR lease_until<now());

-- name: PlatformClaimSendCopy :one
WITH candidate AS (SELECT id FROM mailbox_send_receipts WHERE NOT urmail_restore_quarantine_active() AND status='accepted' AND sent_copy_state IN('pending','unknown') AND raw_secret<>'' AND copy_pending_since>now()-interval '7 days' AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
UPDATE mailbox_send_receipts r SET claim_token=claim_token+1,lease_until=now()+interval '2 minutes',copy_attempts=copy_attempts+1 FROM candidate c WHERE r.id=c.id RETURNING r.*;

-- name: PlatformClaimOwnedSendCopy :one
UPDATE mailbox_send_receipts SET claim_token=claim_token+1,lease_until=now()+interval '2 minutes'
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND status='accepted' AND sent_copy_state IN('pending','unknown','held') AND raw_secret<>'' AND (lease_until IS NULL OR lease_until<now()) RETURNING *;

-- name: PlatformMarkSendCopyUnknown :one
UPDATE mailbox_send_receipts SET sent_copy_state='unknown',updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND claim_token=$4 AND lease_until>now() AND status='accepted' RETURNING *;

-- name: PlatformCompleteSendCopy :one
UPDATE mailbox_send_receipts SET sent_copy_state='complete',raw_secret='',smtp_secret='',last_error='',lease_until=NULL,updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND claim_token=$4 AND lease_until>now() AND status='accepted' RETURNING *;

-- name: PlatformDeferSendCopy :execrows
UPDATE mailbox_send_receipts SET lease_until=NULL,next_attempt_at=now()+make_interval(secs=>LEAST(3600,30*power(2,LEAST(copy_attempts,7)))::int),
 sent_copy_state=CASE WHEN copy_pending_since<=now()-interval '7 days' OR smtp_secret='' THEN 'held' ELSE sent_copy_state END,
 smtp_secret=CASE WHEN copy_pending_since<=now()-interval '7 days' THEN '' ELSE smtp_secret END,last_error='SENT_COPY_PENDING',updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND claim_token=$4 AND status='accepted';

-- name: PlatformDraftSendLocked :one
SELECT EXISTS(SELECT 1 FROM mailbox_send_receipts r WHERE r.tenant_id=$1 AND r.mailbox_id=$2 AND r.draft_folder=$3 AND r.draft_uid=$4 AND r.draft_uidvalidity=$5 AND NOT r.draft_released AND r.status IN('prepared','submitting','accepted','uncertain')) OR EXISTS(SELECT 1 FROM mailbox_scheduled_sends s WHERE s.tenant_id=$1 AND s.mailbox_id=$2 AND s.draft_folder=$3 AND s.draft_uid=$4 AND s.draft_uidvalidity=$5 AND s.status IN('pending','preparing','submitting','uncertain','sent','held')) AS locked;

-- name: PlatformAcknowledgeUncertainSend :execrows
UPDATE mailbox_send_receipts SET draft_released=true,raw_secret='',smtp_secret='',updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND status='uncertain' AND NOT draft_released AND draft_folder=$4 AND draft_uid=$5 AND draft_uidvalidity=$6;

-- name: PlatformLockSendDraft :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(tenant_id)::text||':'||sqlc.arg(mailbox_id)::text||':'||sqlc.arg(folder)::text||':'||sqlc.arg(validity)::text||':'||sqlc.arg(uid)::text, 974152));

-- name: PlatformHoldUnauthorizedSendCopies :execrows
UPDATE mailbox_send_receipts r SET sent_copy_state='held',smtp_secret='',lease_until=NULL,claim_token=claim_token+1,last_error='SENT_COPY_REAUTH_REQUIRED',updated_at=now()
WHERE NOT urmail_restore_quarantine_active() AND r.status='accepted' AND r.sent_copy_state IN('pending','unknown')
AND NOT EXISTS(SELECT 1 FROM mailboxes m JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE m.tenant_id=r.tenant_id AND m.id=r.mailbox_id AND m.status='active' AND t.status='active' AND d.status='active' AND md5(m.password_hash)=r.smtp_password_version);

-- name: PlatformDraftDeleteLocked :one
SELECT EXISTS(SELECT 1 FROM mailbox_send_receipts r WHERE r.tenant_id=$1 AND r.mailbox_id=$2 AND r.draft_folder=$3 AND r.draft_uid=$4 AND r.draft_uidvalidity=$5 AND NOT r.draft_released AND (r.status IN('prepared','submitting','uncertain') OR (r.status='accepted' AND r.sent_copy_state<>'complete'))) OR EXISTS(SELECT 1 FROM mailbox_scheduled_sends s WHERE s.tenant_id=$1 AND s.mailbox_id=$2 AND s.draft_folder=$3 AND s.draft_uid=$4 AND s.draft_uidvalidity=$5 AND (s.status IN('pending','preparing','submitting','uncertain','held') OR (s.status='sent' AND s.sent_copy_state<>'complete'))) AS locked;

-- name: PlatformGetSendReceiptsForDraft :many
SELECT * FROM mailbox_send_receipts WHERE tenant_id=$1 AND mailbox_id=$2 AND draft_folder=$3 AND draft_uid=$4 AND draft_uidvalidity=$5 AND NOT draft_released AND status IN('prepared','submitting','accepted','uncertain') ORDER BY id DESC LIMIT 100;

-- name: PlatformAuthorizeSendCopy :one
SELECT r.id FROM mailbox_send_receipts r
JOIN mailboxes m ON m.tenant_id=r.tenant_id AND m.id=r.mailbox_id
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE r.tenant_id=$1 AND r.mailbox_id=$2 AND r.id=$3 AND r.claim_token=$4 AND r.status='accepted' AND r.lease_until>now()
AND NOT urmail_restore_quarantine_active() AND m.status='active' AND t.status='active' AND d.status='active' AND md5(m.password_hash)=r.smtp_password_version;
