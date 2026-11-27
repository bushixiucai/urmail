-- Mailbox self-service is bound to the independently authenticated webmail session.

-- name: PlatformMailboxAuthLookup :one
-- scope: platform

-- Native webmail login: resolve an active mailbox by address for password verification.
-- Never used for panel identity; Roundcube bridge continues to use HMAC + session create.
SELECT m.tenant_id, m.id AS mailbox_id, m.address, m.password_hash
FROM mailboxes m
JOIN tenants t ON t.id = m.tenant_id
JOIN domains d ON d.tenant_id = m.tenant_id AND d.id = m.domain_id
WHERE m.address = sqlc.arg(address)
  AND m.status = 'active' AND t.status = 'active' AND d.status = 'active';

-- name: PlatformMailboxPasswordForSession :one
-- scope: platform

SELECT m.tenant_id,m.id AS mailbox_id,m.address,m.password_hash,m.password_version
FROM mailbox_sessions s JOIN mailboxes m ON m.tenant_id=s.tenant_id AND m.id=s.mailbox_id
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE s.token_hash=sqlc.arg(session_hash) AND m.address=sqlc.arg(address)
AND s.revoked_at IS NULL AND s.expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND s.password_version=md5(m.password_hash)
AND m.status='active' AND t.status='active' AND d.status='active';

-- name: PlatformMailboxPasswordFailures :one
-- scope: platform

SELECT count(*) FROM audit_logs WHERE tenant_id=sqlc.arg(tenant_id)
AND action='mailbox.password.rejected' AND target_type='mailbox'
AND target_id=sqlc.arg(mailbox_id) AND created_at>(CAST(strftime('%s','now') AS INTEGER)*1000000)-900000000;

-- name: PlatformChangeMailboxPasswordCAS :execrows
-- scope: platform

UPDATE mailboxes SET password_hash=sqlc.arg(password_hash)
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(mailbox_id)
AND address=sqlc.arg(address) AND password_hash=sqlc.arg(expected_hash)
AND password_version=sqlc.arg(expected_version) AND status='active';

-- PlatformRevokeMailboxSessions 在 S7 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- 与手写版等价（UPDATE mailbox_sessions 置 revoked_at，按 tenant_id/mailbox_id，返回 RowsAffected）。

-- PlatformConsumeMailboxOperationTickets 在 S7 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- 与手写版等价（UPDATE mailbox_operation_tickets 置 consumed_at，返回 RowsAffected）。
-- name: PlatformAckMailboxPasswordRevocation :execrows
-- scope: platform

UPDATE mailboxes SET password_revoke_pending=false
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(mailbox_id)
AND address=sqlc.arg(address) AND password_version=sqlc.arg(expected_version)
AND password_revoke_pending;
