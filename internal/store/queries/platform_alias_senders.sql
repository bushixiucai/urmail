-- Privileged, audited maintenance of explicit sender identities; no content access.
-- name: PlatformLockAliasSenderTarget :one
SELECT a.id,a.tenant_id,a.domain_id,a.local_part,a.target,a.kind,a.active,a.sender_logins,d.fqdn AS domain_fqdn
FROM mailbox_aliases a
JOIN domains d ON d.id=a.domain_id AND d.tenant_id=a.tenant_id
JOIN tenants t ON t.id=a.tenant_id AND t.status='active'
JOIN users actor ON actor.id=sqlc.arg(actor_id)::bigint AND actor.status='active'
JOIN platform_admins administrator ON administrator.user_id=actor.id
WHERE a.tenant_id=sqlc.arg(tenant_id)::bigint AND a.id=sqlc.arg(alias_id)::bigint
FOR UPDATE OF a FOR SHARE OF d,t,actor,administrator;

-- name: PlatformLockAliasSenderMailboxes :many
SELECT m.address FROM mailboxes m JOIN domains d ON d.id=m.domain_id AND d.tenant_id=m.tenant_id
WHERE m.tenant_id=sqlc.arg(tenant_id)::bigint AND m.address=ANY(sqlc.arg(addresses)::text[])
 AND m.status='active' AND m.send_enabled AND d.status='active' AND d.provisioning_state='ready'
ORDER BY m.address FOR SHARE OF m,d;

-- name: PlatformSetAliasSenderLogins :execrows
UPDATE mailbox_aliases SET sender_logins=sqlc.arg(sender_logins)::text[]
WHERE tenant_id=sqlc.arg(tenant_id)::bigint AND id=sqlc.arg(alias_id)::bigint;
