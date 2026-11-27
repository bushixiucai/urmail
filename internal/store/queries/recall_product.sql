-- name: RecallMessageCopies :many
-- scope: tenant

SELECT a.id, a.mailbox_id, a.subject, a.message_id, a.legal_hold, coalesce(a.deleted_at, 0) AS deleted_at, m.address, m.status AS mailbox_status, r.id AS recall_id, r.status AS recall_status, r.requested_by
FROM archive_entries a LEFT JOIN mailboxes m ON m.tenant_id=a.tenant_id AND m.id=a.mailbox_id
LEFT JOIN recall_requests r ON r.tenant_id=a.tenant_id AND r.archive_entry_id=a.id
WHERE a.tenant_id=?1 AND a.message_id=(SELECT source.message_id FROM archive_entries source WHERE source.tenant_id=?1 AND source.id=?2)
ORDER BY a.id LIMIT 101;

-- name: StoreRecallReviewPreview :one
-- scope: tenant

INSERT INTO recall_requests(tenant_id, archive_entry_id, mailbox_id, message_id, requested_by, status,
 preview_actor_id, preview_token, preview_digest, preview_snapshot, preview_expires_at, attempt_epoch)
SELECT a.tenant_id, a.id, a.mailbox_id, a.message_id, @actor_id, 'preview', @actor_id, @token, @digest, @snapshot,
 (CAST(strftime('%s','now') AS INTEGER)*1000000)+300000000, 1 FROM archive_entries a
WHERE a.tenant_id= @tenant_id AND a.id= @entry_id
ON CONFLICT(tenant_id, archive_entry_id) DO UPDATE SET status='preview', reason=NULL, completed_at=NULL,
 preview_actor_id=EXCLUDED.preview_actor_id, preview_token=EXCLUDED.preview_token, preview_digest=EXCLUDED.preview_digest,
 preview_snapshot=EXCLUDED.preview_snapshot, preview_expires_at=EXCLUDED.preview_expires_at, confirmed_at=NULL,
 selected_copy_keys='[]', attempt_epoch=recall_requests.attempt_epoch+1
RETURNING id, tenant_id, message_id, requested_by, status, reason, created_at, archive_entry_id, mailbox_id, coalesce(completed_at, 0) AS completed_at, preview_actor_id, preview_token, preview_digest, preview_snapshot, coalesce(preview_expires_at, 0) AS preview_expires_at, coalesce(confirmed_at, 0) AS confirmed_at, selected_copy_keys, attempt_epoch, local_result, result_history;

-- name: GetRecallReviewPreview :one
-- scope: tenant

SELECT id, tenant_id, message_id, requested_by, status, reason, created_at, archive_entry_id, mailbox_id, coalesce(completed_at, 0) AS completed_at, preview_actor_id, preview_token, preview_digest, preview_snapshot, coalesce(preview_expires_at, 0) AS preview_expires_at, coalesce(confirmed_at, 0) AS confirmed_at, selected_copy_keys, attempt_epoch, local_result, result_history FROM recall_requests r WHERE r.tenant_id= @tenant_id AND r.id= @recall_id
 AND r.preview_actor_id= @actor_id AND r.preview_token= @token AND r.preview_digest= @digest;

-- name: ConfirmRecallReview :one
-- scope: tenant

UPDATE recall_requests SET status='pending', confirmed_at=(CAST(strftime('%s','now') AS INTEGER)*1000000), selected_copy_keys= @selected
WHERE tenant_id= @tenant_id AND id= @recall_id AND preview_actor_id= @actor_id AND preview_token= @token AND preview_digest= @digest
 AND preview_expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND status='preview'
RETURNING id, tenant_id, message_id, requested_by, status, reason, created_at, archive_entry_id, mailbox_id, coalesce(completed_at, 0) AS completed_at, preview_actor_id, preview_token, preview_digest, preview_snapshot, coalesce(preview_expires_at, 0) AS preview_expires_at, coalesce(confirmed_at, 0) AS confirmed_at, selected_copy_keys, attempt_epoch, local_result, result_history;
