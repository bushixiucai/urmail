-- name: ListSettingsByScopes :many
-- scope: platform

SELECT scope, scope_id, key, value, updated_at
FROM settings
WHERE key<>'billing_document_profile' AND ((scope = 'platform' AND scope_id = 0)
   OR (scope = 'brand' AND scope_id = ?1)
   OR (scope = 'tenant' AND scope_id = ?2))
ORDER BY key ASC;

-- name: GetTenantSetting :one
-- scope: platform

SELECT scope, scope_id, key, value, updated_at
FROM settings
WHERE scope = 'tenant' AND scope_id = ?1 AND key = ?2;

-- name: UpsertTenantSetting :one
-- scope: tenant
-- scope-reason: tenant-scoped settings row (scope=tenant, scope_id = ctx tenant via RequireTenantTx)

INSERT INTO settings (scope, scope_id, key, value, updated_at)
VALUES ('tenant', ?1, ?2, ?3, (CAST(strftime('%s','now') AS INTEGER)*1000000))
ON CONFLICT (scope, scope_id, key)
DO UPDATE SET value = EXCLUDED.value, updated_at = (CAST(strftime('%s','now') AS INTEGER)*1000000)
RETURNING scope, scope_id, key, value, updated_at;

-- name: DeleteTenantSetting :exec
-- scope: platform

DELETE FROM settings
WHERE scope = 'tenant' AND scope_id = ?1 AND key = ?2;
