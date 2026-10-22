-- name: ListSettingsByScopes :many
SELECT scope, scope_id, key, value, updated_at
FROM settings
WHERE (scope = 'platform' AND scope_id = 0)
   OR (scope = 'brand' AND scope_id = $1)
   OR (scope = 'tenant' AND scope_id = $2)
ORDER BY key ASC;

-- name: GetTenantSetting :one
SELECT scope, scope_id, key, value, updated_at
FROM settings
WHERE scope = 'tenant' AND scope_id = $1 AND key = $2;

-- name: UpsertTenantSetting :one
INSERT INTO settings (scope, scope_id, key, value, updated_at)
VALUES ('tenant', $1, $2, $3, now())
ON CONFLICT (scope, scope_id, key)
DO UPDATE SET value = EXCLUDED.value, updated_at = now()
RETURNING scope, scope_id, key, value, updated_at;

-- name: DeleteTenantSetting :exec
DELETE FROM settings
WHERE scope = 'tenant' AND scope_id = $1 AND key = $2;
