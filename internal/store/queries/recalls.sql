-- name: CreateRecall :one
-- scope: tenant

INSERT INTO recall_requests(tenant_id, archive_entry_id, mailbox_id, message_id, requested_by)
VALUES(?1, ?2, ?3, ?4, ?5)
ON CONFLICT(tenant_id, archive_entry_id) DO NOTHING
RETURNING *;

-- name: GetRecallByEntry :one
-- scope: tenant

SELECT * FROM recall_requests r WHERE r.tenant_id=?1 AND r.archive_entry_id=?2;
-- name: GetRecallByID :one
-- scope: tenant

SELECT * FROM recall_requests WHERE tenant_id=?1 AND id=?2;

-- name: ListRecalls :many
-- scope: tenant

SELECT * FROM recall_requests WHERE tenant_id=?1 ORDER BY id DESC LIMIT 100;

-- name: GetRecallTarget :one
-- scope: tenant

SELECT a.message_id,a.legal_hold,a.deleted_at,m.address,m.id AS mailbox_id,m.status AS mailbox_status,a.tenant_id
FROM archive_entries a LEFT JOIN mailboxes m ON m.id=a.mailbox_id AND m.tenant_id=a.tenant_id
WHERE a.tenant_id=?1 AND a.id=?2;
