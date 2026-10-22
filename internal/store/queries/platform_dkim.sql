-- Platform query for DKIM signing configuration.
-- Exempt from the tenant_id predicate per AGENTS.md (platform_*.sql): the
-- reconcile worker needs every active domain's key to build the signing table.
-- name: PlatformListDKIMDomains :many
SELECT d.fqdn, d.dkim_selector, dkim_private_key
FROM domains d
JOIN tenants t ON t.id = d.tenant_id
WHERE d.status = 'active' AND t.status = 'active'
  AND d.dkim_private_key IS NOT NULL
  AND d.dkim_private_key <> ''
ORDER BY d.fqdn ASC;
