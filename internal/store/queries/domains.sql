-- name: ListDomainsByTenant :many
SELECT id, tenant_id, fqdn, mode, status, dns_provider, provider_ref, verification_token, verified_at, mx_host, tls_mode, dkim_selector, dkim_private_key, created_at
FROM domains
WHERE tenant_id = $1
ORDER BY fqdn ASC;

-- name: GetDomainByID :one
SELECT id, tenant_id, fqdn, mode, status, dns_provider, provider_ref, verification_token, verified_at, mx_host, tls_mode, dkim_selector, dkim_private_key, created_at
FROM domains
WHERE tenant_id = $1 AND id = $2;

-- name: GetDomainByFQDN :one
SELECT id, tenant_id, fqdn, mode, status, dns_provider, provider_ref, verification_token, verified_at, mx_host, tls_mode, dkim_selector, dkim_private_key, created_at
FROM domains
WHERE tenant_id = $1 AND fqdn = $2;

-- name: CreateDomain :one
INSERT INTO domains (
    tenant_id, fqdn, mode, status, dns_provider, provider_ref, verification_token, tls_mode,
    dkim_selector, dkim_private_key
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
) RETURNING id, tenant_id, fqdn, mode, status, dns_provider, provider_ref, verification_token, verified_at, mx_host, tls_mode, dkim_selector, dkim_private_key, created_at;

-- name: UpdateDomainStatus :one
UPDATE domains
SET status = $3, verified_at = $4
WHERE tenant_id = $1 AND id = $2
RETURNING id, tenant_id, fqdn, mode, status, dns_provider, provider_ref, verification_token, verified_at, mx_host, tls_mode, dkim_selector, dkim_private_key, created_at;

-- name: DeleteDomain :exec
DELETE FROM domains
WHERE tenant_id = $1 AND id = $2;

-- name: CountDomainsByTenant :one
SELECT COUNT(*)
FROM domains
WHERE tenant_id = $1;
