-- Append-only audit trail for tenant-visible history.

-- name: CreateAuditLog :one
-- scope: tenant

INSERT INTO audit_logs (
    tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail
) VALUES (
    ?1, ?2, ?3, ?4, ?5, ?6, ?7
) RETURNING id, tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail, created_at;

-- id is the pagination tiebreaker: created_at alone is not stable because
-- several audit rows can share the same timestamp (M6 review D11).
-- The remaining filters are optional (NULL = unfiltered) so the tenant UI can
-- narrow by actor, action or time range without separate endpoints.
-- name: ListAuditLogsByTenant :many
-- scope: tenant

SELECT audit.id, audit.tenant_id, audit.actor_user_id, audit.actor_ip, audit.action, audit.target_type, audit.target_id, audit.detail, audit.created_at
FROM audit_logs AS audit
WHERE audit.tenant_id = @tenant_id
  AND (sqlc.narg('actor_user_id') IS NULL OR audit.actor_user_id = sqlc.narg('actor_user_id'))
  AND (sqlc.narg('action') IS NULL OR audit.action = sqlc.narg('action'))
  AND (sqlc.narg('target_type') IS NULL OR audit.target_type = sqlc.narg('target_type'))
  AND (sqlc.narg('target_id') IS NULL OR audit.target_id = sqlc.narg('target_id'))
  AND (sqlc.narg('from_time') IS NULL OR audit.created_at >= sqlc.narg('from_time'))
  AND (sqlc.narg('to_time') IS NULL OR audit.created_at < sqlc.narg('to_time'))
  AND (sqlc.narg('before_id') IS NULL OR
       (audit.created_at, audit.id) < (
           COALESCE(sqlc.narg('before_time'),
                    (SELECT cursor.created_at FROM audit_logs cursor
                     WHERE cursor.tenant_id = @tenant_id AND cursor.id = sqlc.narg('before_id'))),
           sqlc.narg('before_id')))
ORDER BY audit.created_at DESC, audit.id DESC
LIMIT sqlc.arg('limit');

-- RecordTenantAudit 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- urmail_record_audit = 元数据正则校验 + user_tenant_roles 断言 + 审计插入；
-- current_setting 会话绑定条款由 S2 装饰器承接（app_pending，见 S3 审计附录）。

-- name: GetAuditCursorAnchor :one
-- scope: tenant

SELECT created_at FROM audit_logs WHERE tenant_id = @tenant_id AND id = @id;

-- A single bounded statement gives a consistent export snapshot. The extra
-- row lets the API reject oversized exports without silently truncating them.
-- name: ExportAuditLogsByTenant :many
-- scope: tenant

SELECT id, tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail, created_at
FROM audit_logs
WHERE tenant_id = @tenant_id
  AND (sqlc.narg('actor_user_id') IS NULL OR actor_user_id = sqlc.narg('actor_user_id'))
  AND (sqlc.narg('action') IS NULL OR action = sqlc.narg('action'))
  AND (sqlc.narg('target_type') IS NULL OR target_type = sqlc.narg('target_type'))
  AND (sqlc.narg('target_id') IS NULL OR target_id = sqlc.narg('target_id'))
  AND (sqlc.narg('from_time') IS NULL OR created_at >= sqlc.narg('from_time'))
  AND (sqlc.narg('to_time') IS NULL OR created_at < sqlc.narg('to_time'))
ORDER BY created_at DESC, id DESC LIMIT 10001;
