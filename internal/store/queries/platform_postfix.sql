-- name: PlatformListDomains :many
-- Platform query for Postfix configuration generator.
-- Exempt from tenant_id predicate per AGENTS.md (platform_*.sql).
SELECT d.fqdn, d.status, d.mode, d.updated_at
FROM domains d
JOIN tenants t ON t.id = d.tenant_id
WHERE d.status = 'active' AND t.status = 'active'
ORDER BY d.fqdn ASC;

-- name: PlatformListMailboxes :many
-- Platform query for Postfix configuration generator.
-- Exempt from tenant_id predicate per AGENTS.md (platform_*.sql).
SELECT m.address, m.status
FROM mailboxes m
JOIN tenants t ON t.id = m.tenant_id
WHERE m.status = 'active' AND t.status = 'active'
ORDER BY m.address ASC;

-- name: PlatformListAliases :many
-- Platform query for Postfix configuration generator.
-- Exempt from tenant_id predicate per AGENTS.md (platform_*.sql).
SELECT d.fqdn AS domain, a.local_part, a.target, a.kind
FROM mailbox_aliases a
JOIN domains d ON a.domain_id = d.id
JOIN tenants t ON t.id = a.tenant_id
WHERE a.active = true AND d.status = 'active' AND t.status = 'active'
ORDER BY d.fqdn ASC, a.local_part ASC;
