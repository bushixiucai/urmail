-- name: GetUserByEmail :one
-- scope: platform

SELECT id, email, password_hash, display_name, locale, timezone, status,
       mfa_secret, mfa_recovery_codes, mfa_enabled, created_at, credential_version
FROM users
WHERE email = ?1 AND status = 'active';

-- name: GetUserByID :one
-- scope: platform

SELECT id, email, password_hash, display_name, locale, timezone, status,
       mfa_secret, mfa_recovery_codes, mfa_enabled, created_at, credential_version
FROM users
WHERE id = ?1 AND status = 'active';

-- name: GetUserTenants :many
-- scope: tenant

SELECT t.id, t.brand_id, t.name, t.slug, t.status, r.role
FROM user_tenant_roles r
JOIN tenants t ON r.tenant_id = t.id
WHERE r.user_id = ?1 AND t.status IN ('active', 'pending')
ORDER BY t.name ASC;

-- name: CheckPlatformAdmin :one
-- scope: platform

SELECT EXISTS (
    SELECT 1 FROM platform_admins WHERE user_id = ?1
) AS is_admin;

-- name: CreateSession :one
-- scope: tenant

INSERT INTO sessions (
    id, user_id, tenant_id, csrf_token, user_agent, ip, expires_at, credential_version, origin_host
) SELECT sqlc.arg(id),u.id,sqlc.narg(tenant_id),sqlc.arg(csrf_token),sqlc.narg(user_agent),sqlc.narg(ip),sqlc.arg(expires_at),u.credential_version,sqlc.arg(origin_host)
FROM users u WHERE u.id=sqlc.arg(user_id) AND u.status='active' AND u.password_hash=sqlc.arg(expected_hash) AND u.credential_version=sqlc.arg(expected_version)
RETURNING id, user_id, tenant_id, csrf_token, user_agent, ip, expires_at, created_at, mfa_verified_at, kind, credential_version, origin_host;

-- name: GetSessionByID :one
-- scope: tenant

-- Authentication is valid only while the user is active and, when the
-- session is bound to a tenant, while that tenant is active or pending.
-- This makes disabling a user / suspending a tenant take effect on the next
-- request instead of waiting for the session to expire.
SELECT s.id, s.user_id, s.tenant_id, s.csrf_token, s.user_agent, s.ip, s.expires_at, s.created_at, s.mfa_verified_at, s.kind, s.credential_version, s.origin_host
FROM sessions s
JOIN users u ON u.id = s.user_id AND u.status = 'active'
LEFT JOIN tenants t ON t.id = s.tenant_id
WHERE s.id = ?1 AND s.kind = 'panel'
  AND s.expires_at > (CAST(strftime('%s','now') AS INTEGER)*1000000) AND s.credential_version=u.credential_version
  AND (s.tenant_id IS NULL OR t.status IN ('active', 'pending'));

-- name: DeleteSession :exec
-- scope: tenant
-- scope-reason: 会话按 (id/user_id) 用户域操作，租户由会话行携带（user-scoped）；装饰器按 user 作用域放行

DELETE FROM sessions
WHERE id = ?1;

-- name: DeleteExpiredSessions :exec
-- scope: tenant
-- scope-reason: 过期会话清扫，按时间谓词（系统入口）user-scoped

DELETE FROM sessions
WHERE expires_at <= (CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: GetUserRoleForTenant :one
-- scope: tenant

SELECT r.role
FROM user_tenant_roles r
JOIN users u ON u.id=r.user_id AND u.status='active'
WHERE r.user_id = ?1 AND r.tenant_id = ?2;

-- name: UpdateSessionTenant :exec
-- scope: tenant

UPDATE sessions
SET tenant_id = ?2
WHERE id = ?1;

-- name: SetMFASecret :exec
-- scope: platform

UPDATE users
SET mfa_secret = ?2
WHERE id = ?1;

-- name: EnableUserMFA :exec
-- scope: platform

UPDATE users
SET mfa_enabled = true, mfa_recovery_codes = ?2
WHERE id = ?1;

-- name: DisableUserMFA :exec
-- scope: platform

UPDATE users
SET mfa_enabled = false, mfa_secret = NULL, mfa_recovery_codes = '[]'
WHERE id = ?1;

-- name: ConsumeRecoveryCode :execrows
-- scope: platform

-- Removes one recovery code (given as its stored SHA-256 hex hash) from the
-- user's code list. Zero rows affected means the code did not match.
UPDATE users
SET mfa_recovery_codes = COALESCE((
    SELECT json_group_array(CAST(je.value AS TEXT))
    FROM json_each(CAST(mfa_recovery_codes AS TEXT)) je
    WHERE CAST(je.value AS TEXT) <> ?2
), '[]')
WHERE id = ?1 AND EXISTS(SELECT 1 FROM json_each(CAST(mfa_recovery_codes AS TEXT)) WHERE CAST(value AS TEXT) = ?2);


-- name: UpdateUserPassword :exec
-- scope: platform

UPDATE users SET password_hash = ?2, password_changed_at = (CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE id = ?1;

-- name: DeleteOtherUserSessions :exec
-- scope: tenant
-- scope-reason: 会话按 (id/user_id) 用户域操作；user-scoped

DELETE FROM sessions WHERE user_id = ?1 AND id <> ?2;


-- name: ListSessionsForUser :many
-- scope: tenant

SELECT id, user_id, tenant_id, user_agent, created_at, expires_at
FROM sessions
WHERE user_id = ?1 AND expires_at > (CAST(strftime('%s','now') AS INTEGER)*1000000)
ORDER BY created_at DESC;

-- name: DeleteUserSession :execrows
-- scope: tenant
-- scope-reason: 会话按 (id/user_id) 用户域操作，租户由会话行携带（user-scoped）；装饰器按 user 作用域放行

DELETE FROM sessions WHERE user_id = ?1 AND id = ?2;

