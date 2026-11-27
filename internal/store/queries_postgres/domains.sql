-- name: ListDomainsByTenant :many
SELECT id, tenant_id, fqdn, mode, status, dns_provider, provider_ref, verification_token, verified_at, mx_host, tls_mode, dkim_selector, dkim_private_key, created_at, verification_issued_at, verification_expires_at, provisioning_state, dns_explicit
FROM domains
WHERE tenant_id = $1
ORDER BY fqdn ASC;

-- name: GetDomainByID :one
SELECT id, tenant_id, fqdn, mode, status, dns_provider, provider_ref, verification_token, verified_at, mx_host, tls_mode, dkim_selector, dkim_private_key, created_at, verification_issued_at, verification_expires_at, provisioning_state, dns_explicit
FROM domains
WHERE tenant_id = $1 AND id = $2;

-- name: GetDomainByFQDN :one
SELECT id, tenant_id, fqdn, mode, status, dns_provider, provider_ref, verification_token, verified_at, mx_host, tls_mode, dkim_selector, dkim_private_key, created_at, verification_issued_at, verification_expires_at, provisioning_state, dns_explicit
FROM domains
WHERE tenant_id = $1 AND fqdn = $2;

-- name: CreateDomain :one
INSERT INTO domains (tenant_id, fqdn, mode, status, dns_provider, provider_ref, verification_token, tls_mode, dkim_selector, dkim_private_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, tenant_id, fqdn, mode, status, dns_provider, provider_ref, verification_token, verified_at, mx_host, tls_mode, dkim_selector, dkim_private_key, created_at, verification_issued_at, verification_expires_at, provisioning_state, dns_explicit;

-- name: UpdateDomainStatus :one
UPDATE domains
SET status = $3, verified_at = $4
WHERE tenant_id = $1 AND id = $2 AND ($3 <> 'active' OR $4 IS NOT NULL)
RETURNING id, tenant_id, fqdn, mode, status, dns_provider, provider_ref, verification_token, verified_at, mx_host, tls_mode, dkim_selector, dkim_private_key, created_at, verification_issued_at, verification_expires_at, provisioning_state, dns_explicit;

-- name: DeleteDomain :exec
DELETE FROM domains
WHERE tenant_id = $1 AND id = $2;

-- name: CountDomainsByTenant :one
SELECT COUNT(*)
FROM domains
WHERE tenant_id = $1;

-- name: UpdateDomainModeStatus :one
UPDATE domains
SET mode = $3, status = $4
WHERE tenant_id = $1 AND id = $2
  AND ($4 <> 'active' OR verified_at IS NOT NULL)
  AND ($4 <> 'pending_dns' OR verified_at IS NULL)
RETURNING id, tenant_id, fqdn, mode, status, dns_provider, provider_ref, verification_token, verified_at, mx_host, tls_mode, dkim_selector, dkim_private_key, created_at, verification_issued_at, verification_expires_at, provisioning_state, dns_explicit;

-- name: RotateDomainChallenge :one
UPDATE domains
SET verification_token = $3,
    verification_issued_at = now(),
    verification_expires_at = now() + interval '7 days'
WHERE tenant_id = $1 AND id = $2 AND status <> 'active'
RETURNING id, fqdn, verification_token, verification_issued_at, verification_expires_at, provisioning_state, dns_explicit;

-- name: VerifyDomainChallenge :one
UPDATE domains
SET status = 'active', verified_at = statement_timestamp()
WHERE tenant_id = $1 AND id = $2 AND verification_token = $3
  AND status <> 'active'
  AND verification_issued_at <= statement_timestamp()
  AND verification_expires_at > statement_timestamp()
RETURNING id, tenant_id, fqdn, mode, status, dns_provider, provider_ref,
          verification_token, verified_at, mx_host, tls_mode, dkim_selector,
          dkim_private_key, created_at, verification_issued_at, verification_expires_at, provisioning_state, dns_explicit;
