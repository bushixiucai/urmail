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
-- scope: platform

SELECT t.id, t.brand_id, t.name, t.slug, t.status, r.role
FROM user_tenant_roles r
JOIN tenants t ON r.tenant_id = t.id
WHERE r.user_id = ?1 AND t.status IN ('active', 'pending')
ORDER BY t.name ASC;

-- name: CheckPlatformAdmin :one
-- scope: platform

SELECT CAST(EXISTS (
    SELECT 1 FROM platform_admins WHERE user_id = ?1
) AS BOOLEAN) AS is_admin;

-- name: CreateSession :one
-- scope: platform

INSERT INTO sessions (
    id, user_id, tenant_id, csrf_token, user_agent, ip, expires_at, credential_version, origin_host
) SELECT sqlc.arg(id),u.id,sqlc.narg(tenant_id),sqlc.arg(csrf_token),sqlc.narg(user_agent),sqlc.narg(ip),sqlc.arg(expires_at),u.credential_version,sqlc.arg(origin_host)
FROM users u WHERE u.id=sqlc.arg(user_id) AND u.status='active' AND u.password_hash=sqlc.arg(expected_hash) AND u.credential_version=sqlc.arg(expected_version)
RETURNING id, user_id, tenant_id, csrf_token, user_agent, ip, coalesce(expires_at, 0) AS expires_at, created_at, coalesce(mfa_verified_at, 0) AS mfa_verified_at, kind, credential_version, origin_host;

-- name: GetSessionByID :one
-- scope: platform

-- Authentication is valid only while the user is active and, when the
-- session is bound to a tenant, while that tenant is active or pending.
-- This makes disabling a user / suspending a tenant take effect on the next
-- request instead of waiting for the session to expire.
SELECT s.id, s.user_id, s.tenant_id, s.csrf_token, s.user_agent, s.ip, coalesce(s.expires_at, 0) AS expires_at, s.created_at, coalesce(s.mfa_verified_at, 0) AS mfa_verified_at, s.kind, s.credential_version, s.origin_host
FROM sessions s
JOIN users u ON u.id = s.user_id AND u.status = 'active'
LEFT JOIN tenants t ON t.id = s.tenant_id
WHERE s.id = ?1 AND s.kind = 'panel'
  AND s.expires_at > (CAST(strftime('%s','now') AS INTEGER)*1000000) AND s.credential_version=u.credential_version
  AND (s.tenant_id IS NULL OR t.status IN ('active', 'pending'));

-- name: DeleteSession :exec
-- scope: platform

DELETE FROM sessions
WHERE id = ?1;

-- name: DeleteExpiredSessions :exec
-- scope: system

DELETE FROM sessions
WHERE expires_at <= (CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: GetUserRoleForTenant :one
-- scope: tenant

SELECT r.role
FROM user_tenant_roles r
JOIN users u ON u.id=r.user_id AND u.status='active'
WHERE r.user_id = ?1 AND r.tenant_id = ?2;

-- name: UpdateSessionTenant :exec
-- scope: platform

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


-- name: UpdateUserPassword :exec
-- scope: platform

UPDATE users SET password_hash = ?2, password_changed_at = (CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE id = ?1;

-- name: DeleteOtherUserSessions :exec
-- scope: platform

DELETE FROM sessions WHERE user_id = ?1 AND id <> ?2;


-- name: ListSessionsForUser :many
-- scope: platform

SELECT id, user_id, tenant_id, user_agent, created_at, coalesce(expires_at, 0) AS expires_at
FROM sessions
WHERE user_id = ?1 AND expires_at > (CAST(strftime('%s','now') AS INTEGER)*1000000)
ORDER BY created_at DESC;

-- name: DeleteUserSession :execrows
-- scope: platform

DELETE FROM sessions WHERE user_id = ?1 AND id = ?2;

-- 中文注记（sqlc 的 sqlite 引擎按字节偏移切分语句，任何非 ASCII 注记都必须放在文件末尾）：
-- DeleteSession: scope-reason: 平台认证域会话按 session id 吊销（panel session 的 tenant_id 可空，仅经平台 authQueries 调用）
-- DeleteExpiredSessions: scope-reason: daemon 定时系统级过期会话清扫（system scope，无请求租户）
-- DisableUserMFA: ConsumeRecoveryCode 在 S2 B1b 迁移至 internal/store/sqlite_methods.go
-- DisableUserMFA: ConsumeUserRecoveryCode（JSON 过滤在 Go 层完成，sqlc sqlite 不支持 json_each）。
-- DeleteOtherUserSessions: scope-reason: 平台认证域按 user_id + 当前 session id 吊销其余会话（sessions.tenant_id 可空）
-- DeleteUserSession: scope-reason: 平台认证域按 user_id + session id 吊销单条会话（sessions.tenant_id 可空）
-- GetUserTenants: scope-reason: 平台认证域按 user_id 查询所属租户列表（登录选租户/切换租户前调用，无租户上下文）
-- CreateSession: scope-reason: 平台认证域创建会话（无租户用户/多租户未选定或平台管理员会话的 tenant_id 可为 NULL）
-- GetSessionByID: scope-reason: RequireAuth 鉴权中间件按 token 哈希查询 panel 会话（发生于解析并绑定 ScopeTenant 之前）
-- UpdateSessionTenant: scope-reason: 平台认证域切换当前会话绑定的 tenant_id（由 userLevel 平台认证路由调用）
-- ListSessionsForUser: scope-reason: 平台认证域按 user_id 列出会话（含 tenant_id IS NULL 的无租户/管理员会话）
