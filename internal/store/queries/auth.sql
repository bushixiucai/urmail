-- name: GetUserByEmail :one
SELECT id, email, password_hash, display_name, locale, timezone, status,
       mfa_secret, mfa_recovery_codes, mfa_enabled, created_at
FROM users
WHERE email = $1 AND status = 'active';

-- name: GetUserByID :one
SELECT id, email, password_hash, display_name, locale, timezone, status,
       mfa_secret, mfa_recovery_codes, mfa_enabled, created_at
FROM users
WHERE id = $1 AND status = 'active';

-- name: GetUserTenants :many
SELECT t.id, t.brand_id, t.name, t.slug, t.status, r.role
FROM user_tenant_roles r
JOIN tenants t ON r.tenant_id = t.id
WHERE r.user_id = $1 AND t.status IN ('active', 'pending')
ORDER BY t.name ASC;

-- name: CheckPlatformAdmin :one
SELECT EXISTS (
    SELECT 1 FROM platform_admins WHERE user_id = $1
) AS is_admin;

-- name: CreateSession :one
INSERT INTO sessions (
    id, user_id, tenant_id, csrf_token, user_agent, ip, expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
) RETURNING id, user_id, tenant_id, csrf_token, user_agent, ip, expires_at, created_at;

-- name: GetSessionByID :one
SELECT id, user_id, tenant_id, csrf_token, user_agent, ip, expires_at, created_at
FROM sessions
WHERE id = $1 AND expires_at > now();

-- name: DeleteSession :exec
DELETE FROM sessions
WHERE id = $1;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions
WHERE expires_at <= now();

-- name: GetUserRoleForTenant :one
SELECT r.role
FROM user_tenant_roles r
WHERE r.user_id = $1 AND r.tenant_id = $2;

-- name: UpdateSessionTenant :exec
UPDATE sessions
SET tenant_id = $2
WHERE id = $1;

-- name: SetMFASecret :exec
UPDATE users
SET mfa_secret = $2
WHERE id = $1;

-- name: EnableUserMFA :exec
UPDATE users
SET mfa_enabled = true, mfa_recovery_codes = $2
WHERE id = $1;

-- name: DisableUserMFA :exec
UPDATE users
SET mfa_enabled = false, mfa_secret = NULL, mfa_recovery_codes = '[]'::jsonb
WHERE id = $1;

-- name: ConsumeRecoveryCode :execrows
-- Removes one recovery code (given as its stored SHA-256 hex hash) from the
-- user's code list. Zero rows affected means the code did not match.
UPDATE users
SET mfa_recovery_codes = COALESCE((
    SELECT jsonb_agg(v ORDER BY ord)
    FROM jsonb_array_elements(mfa_recovery_codes) WITH ORDINALITY AS t(v, ord)
    WHERE v <> to_jsonb($2::text)
), '[]'::jsonb)
WHERE id = $1 AND mfa_recovery_codes @> to_jsonb($2::text);

