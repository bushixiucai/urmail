-- name: PlatformMailboxAccessStates :many
SELECT m.tenant_id,m.id AS mailbox_id,m.address,m.password_version,m.password_revoke_pending, (urmail_mail_allowed(m.tenant_id) AND m.status='active' AND t.status='active' AND d.status='active')::boolean AS allowed
FROM mailboxes m JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.id=m.domain_id AND d.tenant_id=m.tenant_id
ORDER BY m.address;

-- name: PlatformNextMailAccessBoundary :one
-- Sending no longer expires with a subscription. There is no planned boundary.
SELECT NULL::timestamptz AS next_boundary;
