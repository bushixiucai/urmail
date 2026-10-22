-- name: PlatformListDomains :many
-- Platform query for Postfix configuration generator.
-- Exempt from tenant_id predicate per AGENTS.md (platform_*.sql).
SELECT fqdn, status, mode, updated_at
FROM domains
WHERE status = 'active'
ORDER BY fqdn ASC;

-- name: PlatformListMailboxes :many
-- Platform query for Postfix configuration generator.
-- Exempt from tenant_id predicate per AGENTS.md (platform_*.sql).
SELECT address, status
FROM mailboxes
WHERE status = 'active'
ORDER BY address ASC;

-- name: PlatformListAliases :many
-- Platform query for Postfix configuration generator.
-- Exempt from tenant_id predicate per AGENTS.md (platform_*.sql).
SELECT d.fqdn AS domain, a.local_part, a.target, a.kind
FROM mailbox_aliases a
JOIN domains d ON a.domain_id = d.id
WHERE a.active = true AND d.status = 'active'
ORDER BY d.fqdn ASC, a.local_part ASC;
