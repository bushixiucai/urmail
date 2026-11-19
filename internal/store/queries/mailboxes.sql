-- name: ListMailboxesByTenant :many
-- scope: tenant

SELECT *
FROM mailboxes
WHERE tenant_id = ?1 AND status <> 'deleted'
ORDER BY address ASC;

-- name: ListMailboxesByDomain :many
-- scope: tenant

SELECT *
FROM mailboxes
WHERE tenant_id = ?1 AND domain_id = ?2 AND status <> 'deleted'
ORDER BY address ASC;

-- name: GetMailboxByID :one
-- scope: tenant

SELECT *
FROM mailboxes
WHERE tenant_id = ?1 AND id = ?2 AND status <> 'deleted';

-- name: GetMailboxByAddress :one
-- scope: tenant

SELECT *
FROM mailboxes
WHERE tenant_id = ?1 AND address = ?2 AND status <> 'deleted';

-- name: CreateMailbox :one
-- scope: tenant

INSERT INTO mailboxes (
    tenant_id, domain_id, local_part, address, display_name, password_hash, quota_bytes, status, send_enabled
) VALUES (
    ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9
) RETURNING *;

-- name: UpdateMailboxPassword :one
-- scope: tenant

UPDATE mailboxes
SET password_hash = ?3
WHERE tenant_id = ?1 AND id = ?2
RETURNING *;

-- name: UpdateMailboxQuota :one
-- scope: tenant

UPDATE mailboxes
SET quota_bytes = ?3
WHERE tenant_id = ?1 AND id = ?2
RETURNING *;

-- name: UpdateMailboxStatus :one
-- scope: tenant

UPDATE mailboxes
SET status = ?3
WHERE tenant_id = ?1 AND id = ?2
RETURNING *;

-- name: DeleteMailbox :exec
-- scope: tenant

UPDATE mailboxes SET status = 'deleted'
WHERE tenant_id = ?1 AND id = ?2 AND status <> 'deleted';

-- name: CountMailboxesByTenant :one
-- scope: tenant

SELECT COUNT(*)
FROM mailboxes
WHERE tenant_id = ?1 AND status <> 'deleted';

-- name: UpdateMailboxSettings :one
-- scope: tenant

UPDATE mailboxes
SET display_name = ?3, status = ?4, send_enabled = ?5
WHERE tenant_id = ?1 AND id = ?2
RETURNING id, tenant_id, domain_id, local_part, address, display_name, password_hash, quota_bytes, status, send_enabled, created_at;

