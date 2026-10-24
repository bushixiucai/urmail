-- Append-only audit trail for tenant-visible history.

-- name: CreateAuditLog :one
INSERT INTO audit_logs (
    tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
) RETURNING id, tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail, created_at;

-- id is the pagination tiebreaker: created_at alone is not stable because
-- several audit rows can share the same timestamp (M6 review D11).
-- The remaining filters are optional (NULL = unfiltered) so the tenant UI can
-- narrow by actor, action or time range without separate endpoints.
-- name: ListAuditLogsByTenant :many
SELECT id, tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail, created_at
FROM audit_logs
WHERE tenant_id = @tenant_id
  AND (sqlc.narg('actor_user_id')::bigint IS NULL OR actor_user_id = sqlc.narg('actor_user_id'))
  AND (sqlc.narg('action')::text IS NULL OR action = sqlc.narg('action'))
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time'))
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time'))
  AND (sqlc.narg('before_id')::bigint IS NULL OR id < sqlc.narg('before_id'))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('limit');
