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
SELECT audit.id, audit.tenant_id, audit.actor_user_id, audit.actor_ip, audit.action, audit.target_type, audit.target_id, audit.detail, audit.created_at
FROM audit_logs AS audit
WHERE audit.tenant_id = @tenant_id
  AND (sqlc.narg('actor_user_id')::bigint IS NULL OR audit.actor_user_id = sqlc.narg('actor_user_id'))
  AND (sqlc.narg('action')::text IS NULL OR audit.action = sqlc.narg('action'))
  AND (sqlc.narg('target_type')::text IS NULL OR audit.target_type = sqlc.narg('target_type'))
  AND (sqlc.narg('target_id')::text IS NULL OR audit.target_id = sqlc.narg('target_id'))
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR audit.created_at >= sqlc.narg('from_time'))
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR audit.created_at < sqlc.narg('to_time'))
  AND (sqlc.narg('before_id')::bigint IS NULL OR
       (audit.created_at, audit.id) < (
           COALESCE(sqlc.narg('before_time')::timestamptz,
                    (SELECT cursor.created_at FROM audit_logs cursor
                     WHERE cursor.tenant_id = @tenant_id AND cursor.id = sqlc.narg('before_id'))),
           sqlc.narg('before_id')::bigint))
ORDER BY audit.created_at DESC, audit.id DESC
LIMIT sqlc.arg('limit');

-- name: RecordTenantAudit :exec
SELECT urmail_record_audit(@tenant_id::bigint, @user_id::bigint,
    sqlc.narg('actor_ip')::inet, @action::text, @target::text, @status::integer);

-- name: GetAuditCursorAnchor :one
SELECT created_at FROM audit_logs WHERE tenant_id = @tenant_id AND id = @id;

-- A single bounded statement gives a consistent export snapshot. The extra
-- row lets the API reject oversized exports without silently truncating them.
-- name: ExportAuditLogsByTenant :many
SELECT id, tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail, created_at
FROM audit_logs
WHERE tenant_id = @tenant_id
  AND (sqlc.narg('actor_user_id')::bigint IS NULL OR actor_user_id = sqlc.narg('actor_user_id'))
  AND (sqlc.narg('action')::text IS NULL OR action = sqlc.narg('action'))
  AND (sqlc.narg('target_type')::text IS NULL OR target_type = sqlc.narg('target_type'))
  AND (sqlc.narg('target_id')::text IS NULL OR target_id = sqlc.narg('target_id'))
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time'))
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time'))
ORDER BY created_at DESC, id DESC LIMIT 10001;
