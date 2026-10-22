-- Platform query for DKIM signing configuration.
-- Exempt from the tenant_id predicate per AGENTS.md (platform_*.sql): the
-- reconcile worker needs every active domain's key to build the signing table.
-- name: PlatformListDKIMDomains :many
SELECT fqdn, dkim_selector, dkim_private_key
FROM domains
WHERE status = 'active'
  AND dkim_private_key IS NOT NULL
  AND dkim_private_key <> ''
ORDER BY fqdn ASC;
