-- name: ListMailboxesByTenant :many
SELECT id, tenant_id, domain_id, local_part, address, display_name, password_hash, quota_bytes, status, send_enabled, sieve_script, created_at
FROM mailboxes
WHERE tenant_id = $1
ORDER BY address ASC;

-- name: ListMailboxesByDomain :many
SELECT id, tenant_id, domain_id, local_part, address, display_name, password_hash, quota_bytes, status, send_enabled, sieve_script, created_at
FROM mailboxes
WHERE tenant_id = $1 AND domain_id = $2
ORDER BY address ASC;

-- name: GetMailboxByID :one
SELECT id, tenant_id, domain_id, local_part, address, display_name, password_hash, quota_bytes, status, send_enabled, sieve_script, created_at
FROM mailboxes
WHERE tenant_id = $1 AND id = $2;

-- name: GetMailboxByAddress :one
SELECT id, tenant_id, domain_id, local_part, address, display_name, password_hash, quota_bytes, status, send_enabled, sieve_script, created_at
FROM mailboxes
WHERE tenant_id = $1 AND address = $2;

-- name: CreateMailbox :one
INSERT INTO mailboxes (
    tenant_id, domain_id, local_part, address, display_name, password_hash, quota_bytes, status, send_enabled
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
) RETURNING id, tenant_id, domain_id, local_part, address, display_name, password_hash, quota_bytes, status, send_enabled, sieve_script, created_at;

-- name: UpdateMailboxPassword :one
UPDATE mailboxes
SET password_hash = $3
WHERE tenant_id = $1 AND id = $2
RETURNING id, tenant_id, domain_id, local_part, address, display_name, password_hash, quota_bytes, status, send_enabled, sieve_script, created_at;

-- name: UpdateMailboxQuota :one
UPDATE mailboxes
SET quota_bytes = $3
WHERE tenant_id = $1 AND id = $2
RETURNING id, tenant_id, domain_id, local_part, address, display_name, password_hash, quota_bytes, status, send_enabled, sieve_script, created_at;

-- name: UpdateMailboxStatus :one
UPDATE mailboxes
SET status = $3
WHERE tenant_id = $1 AND id = $2
RETURNING id, tenant_id, domain_id, local_part, address, display_name, password_hash, quota_bytes, status, send_enabled, sieve_script, created_at;

-- name: DeleteMailbox :exec
DELETE FROM mailboxes
WHERE tenant_id = $1 AND id = $2;

-- name: CountMailboxesByTenant :one
SELECT COUNT(*)
FROM mailboxes
WHERE tenant_id = $1;

-- name: UpdateMailboxSettings :one
UPDATE mailboxes
SET display_name = $3, status = $4, send_enabled = $5
WHERE tenant_id = $1 AND id = $2
RETURNING id, tenant_id, domain_id, local_part, address, display_name, password_hash, quota_bytes, status, send_enabled, created_at;

