-- name: ListAliasesByTenant :many
SELECT a.id, a.tenant_id, a.domain_id, a.local_part, a.target, a.kind, a.active, a.created_at, d.fqdn AS domain_fqdn
FROM mailbox_aliases a
JOIN domains d ON a.domain_id = d.id
WHERE a.tenant_id = $1
ORDER BY d.fqdn ASC, a.local_part ASC;

-- name: GetAliasByID :one
SELECT a.id, a.tenant_id, a.domain_id, a.local_part, a.target, a.kind, a.active, a.created_at, d.fqdn AS domain_fqdn
FROM mailbox_aliases a
JOIN domains d ON a.domain_id = d.id
WHERE a.tenant_id = $1 AND a.id = $2;

-- name: GetAliasByDomainAndLocalPart :one
SELECT a.id, a.tenant_id, a.domain_id, a.local_part, a.target, a.kind, a.active, a.created_at, d.fqdn AS domain_fqdn
FROM mailbox_aliases a
JOIN domains d ON a.domain_id = d.id
WHERE a.tenant_id = $1 AND a.domain_id = $2 AND a.local_part = $3;

-- name: CreateAlias :one
INSERT INTO mailbox_aliases (
    tenant_id, domain_id, local_part, target, kind, active
) VALUES (
    $1, $2, $3, $4, $5, $6
) RETURNING id, tenant_id, domain_id, local_part, target, kind, active, created_at;

-- name: UpdateAlias :one
UPDATE mailbox_aliases
SET local_part = $3, target = $4, kind = $5, active = $6
WHERE tenant_id = $1 AND id = $2
RETURNING id, tenant_id, domain_id, local_part, target, kind, active, created_at;

-- name: DeleteAlias :exec
DELETE FROM mailbox_aliases
WHERE tenant_id = $1 AND id = $2;
