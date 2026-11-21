-- Compliance content grants and retention policies.
-- Provenance columns (granted_by / revoked_by / revoked_at / created_by) are
-- maintained by the handlers: a grant must always record who issued it, and a
-- revocation must record who revoked it (M6 review D6/D24).

-- name: GetComplianceGrant :one
SELECT grant_row.id, grant_row.tenant_id, grant_row.user_id, grant_row.scope, grant_row.expires_at, grant_row.revoked, grant_row.reason, grant_row.granted_by, grant_row.revoked_by, grant_row.revoked_at, grant_row.created_at, grant_row.updated_at, grant_row.owner_confirmed_at
FROM compliance_grants AS grant_row
WHERE grant_row.tenant_id = @tenant_id AND grant_row.user_id = @user_id
  AND grant_row.scope = @required_scope::text AND grant_row.revoked = false
  AND grant_row.owner_confirmed_at IS NOT NULL
  AND (grant_row.scope = 'metadata_only' OR
       (grant_row.expires_at > statement_timestamp() AND grant_row.granted_by IS NOT NULL AND length(btrim(grant_row.reason)) > 0
        AND EXISTS (SELECT 1 FROM user_tenant_roles r
                    WHERE r.tenant_id = grant_row.tenant_id
                      AND r.user_id = grant_row.user_id AND r.role IN ('owner','admin'))))
ORDER BY created_at DESC LIMIT 1 FOR SHARE OF grant_row;

-- name: CreateComplianceGrant :one
INSERT INTO compliance_grants (
    tenant_id, user_id, scope, expires_at, reason, granted_by
) VALUES (
    $1, $2, $3, $4, $5, $6
) RETURNING id, tenant_id, user_id, scope, expires_at, revoked, reason, granted_by, revoked_by, revoked_at, created_at, updated_at, owner_confirmed_at;

-- name: RevokeComplianceGrant :one
UPDATE compliance_grants
SET revoked = true, updated_at = now(), revoked_by = $3, revoked_at = now()
WHERE tenant_id = $1 AND id = $2
RETURNING id, tenant_id, user_id, scope, expires_at, revoked, reason, granted_by, revoked_by, revoked_at, created_at, updated_at, owner_confirmed_at;

-- name: ListComplianceGrantsByTenant :many
SELECT id, tenant_id, user_id, scope, expires_at, revoked, reason, granted_by, revoked_by, revoked_at, created_at, updated_at, owner_confirmed_at
FROM compliance_grants
WHERE tenant_id = $1
ORDER BY created_at DESC;

-- name: GetRetentionPolicy :one
SELECT id, tenant_id, retention_days, action, active, created_by, created_at, updated_at, confirmed_preview, authorization_grant_id
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
RETURNING id, tenant_id, retention_days, action, active, created_by, created_at, updated_at, confirmed_preview, authorization_grant_id;

-- name: ListComplianceGrantRecipients :many
SELECT u.id, u.email, r.role FROM users u
JOIN user_tenant_roles r ON r.user_id = u.id
WHERE r.tenant_id = @tenant_id AND r.role IN ('owner', 'admin')
ORDER BY u.email LIMIT 500;

-- name: DisableTenantRetentionPolicy :exec
UPDATE retention_policies SET active=false,updated_at=statement_timestamp() WHERE tenant_id = @tenant_id;

-- name: GetRetentionPolicyAuthority :one
SELECT urmail_retention_authorized(@tenant_id::bigint,@created_by::bigint,@grant_id::bigint)::boolean AS allowed;
