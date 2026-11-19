-- Privileged, audited maintenance of explicit sender identities; no content access.
-- name: PlatformLockAliasSenderTarget :one
-- scope: platform

SELECT a.id,a.tenant_id,a.domain_id,a.local_part,a.target,a.kind,a.active,a.sender_logins,d.fqdn AS domain_fqdn
FROM mailbox_aliases a
JOIN domains d ON d.id=a.domain_id AND d.tenant_id=a.tenant_id
JOIN tenants t ON t.id=a.tenant_id AND t.status='active'
JOIN users actor ON actor.id=sqlc.arg(actor_id) AND actor.status='active'
JOIN platform_admins administrator ON administrator.user_id=actor.id
WHERE a.tenant_id=sqlc.arg(tenant_id) AND a.id=sqlc.arg(alias_id);

-- name: PlatformLockAliasSenderMailboxes :many
-- scope: platform

SELECT m.address FROM mailboxes m JOIN domains d ON d.id=m.domain_id AND d.tenant_id=m.tenant_id
WHERE m.tenant_id=sqlc.arg(tenant_id) AND EXISTS(SELECT 1 FROM json_each(sqlc.arg(addresses)) je WHERE je.value = m.address)
 AND m.status='active' AND m.send_enabled AND d.status='active' AND d.provisioning_state='ready'
ORDER BY m.address;

-- name: PlatformSetAliasSenderLogins :execrows
-- scope: platform

UPDATE mailbox_aliases SET sender_logins=sqlc.arg(sender_logins)
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(alias_id);
