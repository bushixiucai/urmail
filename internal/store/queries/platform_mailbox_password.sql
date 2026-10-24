-- Mailbox self-service is bound to the independently authenticated webmail session.
-- name: PlatformMailboxPasswordForSession :one
SELECT m.tenant_id,m.id AS mailbox_id,m.address,m.password_hash,m.password_version
FROM mailbox_sessions s JOIN mailboxes m ON m.tenant_id=s.tenant_id AND m.id=s.mailbox_id
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE s.token_hash=sqlc.arg(session_hash)::text AND m.address=sqlc.arg(address)::text
AND s.revoked_at IS NULL AND s.expires_at>now() AND s.password_version=md5(m.password_hash)
AND m.status='active' AND t.status='active' AND d.status='active'
FOR UPDATE OF m;

-- name: PlatformMailboxPasswordFailures :one
SELECT count(*) FROM audit_logs WHERE tenant_id=sqlc.arg(tenant_id)::bigint
AND action='mailbox.password.rejected' AND target_type='mailbox'
AND target_id=sqlc.arg(mailbox_id)::bigint::text AND created_at>now()-interval '15 minutes';

-- name: PlatformChangeMailboxPasswordCAS :execrows
UPDATE mailboxes SET password_hash=sqlc.arg(password_hash)::text
WHERE tenant_id=sqlc.arg(tenant_id)::bigint AND id=sqlc.arg(mailbox_id)::bigint
AND address=sqlc.arg(address)::text AND password_hash=sqlc.arg(expected_hash)::text
AND password_version=sqlc.arg(expected_version)::bigint AND status='active';

-- name: PlatformRevokeMailboxCredentials :exec
WITH revoked_sessions AS (
 UPDATE mailbox_sessions SET revoked_at=COALESCE(revoked_at,now())
 WHERE tenant_id=sqlc.arg(tenant_id)::bigint AND mailbox_id=sqlc.arg(mailbox_id)::bigint RETURNING token_hash
)
UPDATE mailbox_operation_tickets SET consumed_at=COALESCE(consumed_at,now())
WHERE tenant_id=sqlc.arg(tenant_id)::bigint AND mailbox_id=sqlc.arg(mailbox_id)::bigint;

-- name: PlatformAckMailboxPasswordRevocation :execrows
UPDATE mailboxes SET password_revoke_pending=false
WHERE tenant_id=sqlc.arg(tenant_id)::bigint AND id=sqlc.arg(mailbox_id)::bigint
AND address=sqlc.arg(address)::text AND password_version=sqlc.arg(expected_version)::bigint
AND password_revoke_pending;
