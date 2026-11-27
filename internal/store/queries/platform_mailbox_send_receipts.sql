-- name: PlatformCreateSendReceipt :one
-- scope: platform

INSERT INTO mailbox_send_receipts(tenant_id,mailbox_id,request_key,request_hash,message_id,raw_secret,smtp_secret,draft_folder,draft_uid,draft_uidvalidity,smtp_password_version,lease_until)
SELECT sqlc.arg(tenant_id),sqlc.arg(mailbox_id),sqlc.arg(request_key),sqlc.arg(request_hash),sqlc.arg(message_id),sqlc.arg(raw_secret),sqlc.arg(smtp_secret),sqlc.arg(draft_folder),sqlc.arg(draft_uid),sqlc.arg(draft_uidvalidity),(SELECT md5(m.password_hash) FROM mailboxes m WHERE m.tenant_id=sqlc.arg(tenant_id) AND m.id=sqlc.arg(mailbox_id)),(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0)
ON CONFLICT(tenant_id,mailbox_id,request_key) DO NOTHING RETURNING *;

-- name: PlatformGetSendReceipt :one
-- scope: platform

SELECT * FROM mailbox_send_receipts WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3;

-- name: PlatformGetSendReceiptByKey :one
-- scope: platform

SELECT * FROM mailbox_send_receipts WHERE tenant_id=?1 AND mailbox_id=?2 AND request_key=?3;

-- name: PlatformListSendReceipts :many
-- scope: platform

SELECT * FROM mailbox_send_receipts WHERE tenant_id=?1 AND mailbox_id=?2 ORDER BY id DESC LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: PlatformBeginSendReceipt :one
-- scope: platform

UPDATE mailbox_send_receipts SET status='submitting',claim_token=claim_token+1,lease_until=(CAST(strftime('%s','now') AS INTEGER)*1000000)+120000000,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id) AND status='prepared' AND lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000)
AND EXISTS(SELECT 1 FROM mailboxes m JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE m.tenant_id=tenant_id AND m.id=mailbox_id AND m.status='active' AND t.status='active' AND d.status='active') RETURNING *;

-- name: PlatformFinishSendReceipt :one
-- scope: platform

UPDATE mailbox_send_receipts SET status=sqlc.arg(final_status),last_error=sqlc.arg(last_error),
 accepted_at=CASE WHEN sqlc.arg(final_status)='accepted' THEN (CAST(strftime('%s','now') AS INTEGER)*1000000) ELSE accepted_at END,
 sent_copy_state=CASE WHEN sqlc.arg(final_status)='accepted' THEN 'pending' ELSE 'none' END,
 copy_pending_since=CASE WHEN sqlc.arg(final_status)='accepted' THEN COALESCE(copy_pending_since,(CAST(strftime('%s','now') AS INTEGER)*1000000)) ELSE copy_pending_since END,
 smtp_secret=CASE WHEN sqlc.arg(final_status)='accepted' THEN smtp_secret ELSE '' END,
 raw_secret=CASE WHEN sqlc.arg(final_status)='rejected' THEN '' ELSE raw_secret END,
 lease_until=NULL,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id) AND claim_token=sqlc.arg(claim_token) AND status='submitting' RETURNING *;

-- name: PlatformRecoverSendReceipts :execrows
-- scope: platform

UPDATE mailbox_send_receipts SET status=CASE WHEN status='prepared' THEN 'rejected' ELSE 'uncertain' END,
 last_error=CASE WHEN status='prepared' THEN 'SEND_NOT_SUBMITTED' ELSE 'MAILBOX_SEND_UNCERTAIN' END,
 raw_secret=CASE WHEN status='prepared' THEN '' ELSE raw_secret END,smtp_secret='',claim_token=claim_token+1,lease_until=NULL,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND status IN('prepared','submitting') AND lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: PlatformHoldExpiredSendCopies :execrows
-- scope: platform

UPDATE mailbox_send_receipts SET sent_copy_state='held',smtp_secret='',last_error='SENT_COPY_PENDING',claim_token=claim_token+1,lease_until=NULL,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND status='accepted' AND sent_copy_state IN('pending','unknown') AND copy_pending_since<=(CAST(strftime('%s','now') AS INTEGER)*1000000)-604800000000 AND (lease_until IS NULL OR lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000));

-- name: PlatformClaimSendCopy :one
-- scope: platform

WITH candidate AS (SELECT id FROM mailbox_send_receipts WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND status='accepted' AND sent_copy_state IN('pending','unknown') AND raw_secret<>'' AND copy_pending_since>(CAST(strftime('%s','now') AS INTEGER)*1000000)-604800000000 AND next_attempt_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000) AND (lease_until IS NULL OR lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000)) ORDER BY next_attempt_at,id LIMIT 1)
UPDATE mailbox_send_receipts SET claim_token=claim_token+1,lease_until=(CAST(strftime('%s','now') AS INTEGER)*1000000)+120000000,copy_attempts=copy_attempts+1
WHERE id=(SELECT id FROM candidate) AND (lease_until IS NULL OR lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000))
RETURNING *;

-- name: PlatformClaimOwnedSendCopy :one
-- scope: platform

UPDATE mailbox_send_receipts SET claim_token=claim_token+1,lease_until=(CAST(strftime('%s','now') AS INTEGER)*1000000)+120000000
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND status='accepted' AND sent_copy_state IN('pending','unknown','held') AND raw_secret<>'' AND (lease_until IS NULL OR lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000)) RETURNING *;

-- name: PlatformMarkSendCopyUnknown :one
-- scope: platform

UPDATE mailbox_send_receipts SET sent_copy_state='unknown',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND claim_token=?4 AND lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND status='accepted' RETURNING *;

-- name: PlatformCompleteSendCopy :one
-- scope: platform

UPDATE mailbox_send_receipts SET sent_copy_state='complete',raw_secret='',smtp_secret='',last_error='',lease_until=NULL,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND claim_token=?4 AND lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND status='accepted' RETURNING *;

-- name: PlatformDeferSendCopy :execrows
-- scope: platform

UPDATE mailbox_send_receipts SET lease_until=NULL,next_attempt_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)+(min(3600, 30*(1<<min(copy_attempts,7)))*1000000),
 sent_copy_state=CASE WHEN copy_pending_since<=(CAST(strftime('%s','now') AS INTEGER)*1000000)-604800000000 OR smtp_secret='' THEN 'held' ELSE sent_copy_state END,
 smtp_secret=CASE WHEN copy_pending_since<=(CAST(strftime('%s','now') AS INTEGER)*1000000)-604800000000 THEN '' ELSE smtp_secret END,last_error='SENT_COPY_PENDING',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND claim_token=?4 AND status='accepted';

-- name: PlatformDraftSendLocked :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM mailbox_send_receipts r WHERE r.tenant_id=?1 AND r.mailbox_id=?2 AND r.draft_folder=?3 AND r.draft_uid=?4 AND r.draft_uidvalidity=?5 AND NOT r.draft_released AND r.status IN('prepared','submitting','accepted','uncertain')) OR EXISTS(SELECT 1 FROM mailbox_scheduled_sends s WHERE s.tenant_id=?1 AND s.mailbox_id=?2 AND s.draft_folder=?3 AND s.draft_uid=?4 AND s.draft_uidvalidity=?5 AND s.status IN('pending','preparing','submitting','uncertain','sent','held')) AS locked;

-- name: PlatformAcknowledgeUncertainSend :execrows
-- scope: platform

UPDATE mailbox_send_receipts SET draft_released=true,raw_secret='',smtp_secret='',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND status='uncertain' AND NOT draft_released AND draft_folder=?4 AND draft_uid=?5 AND draft_uidvalidity=?6;

-- PlatformLockSendDraft 在 S5 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- PG 用事务级 advisory lock 串行化草稿发送与草稿编辑；SQLite 改为单写者事务串行
-- （BEGIN IMMEDIATE），详见 scripts/sqlite/lock-semantics.md。


-- name: PlatformHoldUnauthorizedSendCopies :execrows
-- scope: platform

UPDATE mailbox_send_receipts AS r SET sent_copy_state='held',smtp_secret='',lease_until=NULL,claim_token=claim_token+1,last_error='SENT_COPY_REAUTH_REQUIRED',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND r.status='accepted' AND r.sent_copy_state IN('pending','unknown')
AND NOT EXISTS(SELECT 1 FROM mailboxes m JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id WHERE m.tenant_id=r.tenant_id AND m.id=r.mailbox_id AND m.status='active' AND t.status='active' AND d.status='active' AND md5(m.password_hash)=r.smtp_password_version);

-- name: PlatformDraftDeleteLocked :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM mailbox_send_receipts r WHERE r.tenant_id=?1 AND r.mailbox_id=?2 AND r.draft_folder=?3 AND r.draft_uid=?4 AND r.draft_uidvalidity=?5 AND NOT r.draft_released AND (r.status IN('prepared','submitting','uncertain') OR (r.status='accepted' AND r.sent_copy_state<>'complete'))) OR EXISTS(SELECT 1 FROM mailbox_scheduled_sends s WHERE s.tenant_id=?1 AND s.mailbox_id=?2 AND s.draft_folder=?3 AND s.draft_uid=?4 AND s.draft_uidvalidity=?5 AND (s.status IN('pending','preparing','submitting','uncertain','held') OR (s.status='sent' AND s.sent_copy_state<>'complete'))) AS locked;

-- name: PlatformGetSendReceiptsForDraft :many
-- scope: platform

SELECT * FROM mailbox_send_receipts WHERE tenant_id=?1 AND mailbox_id=?2 AND draft_folder=?3 AND draft_uid=?4 AND draft_uidvalidity=?5 AND NOT draft_released AND status IN('prepared','submitting','accepted','uncertain') ORDER BY id DESC LIMIT 100;

-- name: PlatformAuthorizeSendCopy :one
-- scope: platform

SELECT r.id FROM mailbox_send_receipts r
JOIN mailboxes m ON m.tenant_id=r.tenant_id AND m.id=r.mailbox_id
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE r.tenant_id=?1 AND r.mailbox_id=?2 AND r.id=?3 AND r.claim_token=?4 AND r.status='accepted' AND r.lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000)
AND NOT COALESCE((SELECT active FROM restore_quarantine), 0) AND m.status='active' AND t.status='active' AND d.status='active' AND md5(m.password_hash)=r.smtp_password_version;
