-- name: CreateRecall :one
-- scope: tenant

INSERT INTO recall_requests(tenant_id, archive_entry_id, mailbox_id, message_id, requested_by)
VALUES(?1, ?2, ?3, ?4, ?5)
ON CONFLICT(tenant_id, archive_entry_id) DO NOTHING
RETURNING id, tenant_id, message_id, requested_by, status, reason, created_at, archive_entry_id, mailbox_id, coalesce(completed_at, 0) AS completed_at, preview_actor_id, preview_token, preview_digest, preview_snapshot, coalesce(preview_expires_at, 0) AS preview_expires_at, coalesce(confirmed_at, 0) AS confirmed_at, selected_copy_keys, attempt_epoch, local_result, result_history;

-- name: GetRecallByEntry :one
-- scope: tenant

SELECT id, tenant_id, message_id, requested_by, status, reason, created_at, archive_entry_id, mailbox_id, coalesce(completed_at, 0) AS completed_at, preview_actor_id, preview_token, preview_digest, preview_snapshot, coalesce(preview_expires_at, 0) AS preview_expires_at, coalesce(confirmed_at, 0) AS confirmed_at, selected_copy_keys, attempt_epoch, local_result, result_history FROM recall_requests r WHERE r.tenant_id=?1 AND r.archive_entry_id=?2;
-- name: GetRecallByID :one
-- scope: tenant

SELECT id, tenant_id, message_id, requested_by, status, reason, created_at, archive_entry_id, mailbox_id, coalesce(completed_at, 0) AS completed_at, preview_actor_id, preview_token, preview_digest, preview_snapshot, coalesce(preview_expires_at, 0) AS preview_expires_at, coalesce(confirmed_at, 0) AS confirmed_at, selected_copy_keys, attempt_epoch, local_result, result_history FROM recall_requests WHERE tenant_id=?1 AND id=?2;

-- name: ListRecalls :many
-- scope: tenant

SELECT id, tenant_id, message_id, requested_by, status, reason, created_at, archive_entry_id, mailbox_id, coalesce(completed_at, 0) AS completed_at, preview_actor_id, preview_token, preview_digest, preview_snapshot, coalesce(preview_expires_at, 0) AS preview_expires_at, coalesce(confirmed_at, 0) AS confirmed_at, selected_copy_keys, attempt_epoch, local_result, result_history FROM recall_requests WHERE tenant_id=?1 ORDER BY id DESC LIMIT 100;

-- name: GetRecallTarget :one
-- scope: tenant

SELECT a.message_id, a.legal_hold, coalesce(a.deleted_at, 0) AS deleted_at, m.address, m.id AS mailbox_id, m.status AS mailbox_status, a.tenant_id
FROM archive_entries a LEFT JOIN mailboxes m ON m.id=a.mailbox_id AND m.tenant_id=a.tenant_id
WHERE a.tenant_id=?1 AND a.id=?2;
