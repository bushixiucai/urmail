-- Compliance content grants and retention policies.
-- Provenance columns (granted_by / revoked_by / revoked_at / created_by) are
-- maintained by the handlers: a grant must always record who issued it, and a
-- revocation must record who revoked it (M6 review D6/D24).

-- name: GetComplianceGrant :one
SELECT id, tenant_id, user_id, scope, expires_at, revoked, reason, granted_by, revoked_by, revoked_at, created_at, updated_at
FROM compliance_grants
WHERE tenant_id = $1 AND user_id = $2 AND revoked = false AND (expires_at IS NULL OR expires_at > now())
ORDER BY created_at DESC LIMIT 1;

-- name: CreateComplianceGrant :one
INSERT INTO compliance_grants (
    tenant_id, user_id, scope, expires_at, reason, granted_by
) VALUES (
    $1, $2, $3, $4, $5, $6
) RETURNING id, tenant_id, user_id, scope, expires_at, revoked, reason, granted_by, revoked_by, revoked_at, created_at, updated_at;

-- name: RevokeComplianceGrant :one
UPDATE compliance_grants
SET revoked = true, updated_at = now(), revoked_by = $3, revoked_at = now()
WHERE tenant_id = $1 AND id = $2
RETURNING id, tenant_id, user_id, scope, expires_at, revoked, reason, granted_by, revoked_by, revoked_at, created_at, updated_at;

-- name: ListComplianceGrantsByTenant :many
SELECT id, tenant_id, user_id, scope, expires_at, revoked, reason, granted_by, revoked_by, revoked_at, created_at, updated_at
FROM compliance_grants
WHERE tenant_id = $1
ORDER BY created_at DESC;

-- name: GetRetentionPolicy :one
SELECT id, tenant_id, retention_days, action, active, created_by, created_at, updated_at
FROM retention_policies
WHERE tenant_id = $1;

-- name: UpsertRetentionPolicy :one
INSERT INTO retention_policies (
    tenant_id, retention_days, action, active, created_by
) VALUES (
    $1, $2, $3, $4, $5
) ON CONFLICT (tenant_id) DO UPDATE
SET retention_days = EXCLUDED.retention_days,
    action = EXCLUDED.action,
    active = EXCLUDED.active,
    updated_at = now()
RETURNING id, tenant_id, retention_days, action, active, created_by, created_at, updated_at;
