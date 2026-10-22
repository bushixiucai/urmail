-- name: CreateAuditLog :one
INSERT INTO audit_logs (
    tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
) RETURNING id, tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail, created_at;

-- name: ListAuditLogsByTenant :many
SELECT id, tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail, created_at
FROM audit_logs
WHERE tenant_id = $1
ORDER BY created_at DESC
LIMIT $2;
