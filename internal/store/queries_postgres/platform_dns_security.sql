-- name: PlatformListDNSSecurity :many
SELECT d.fqdn,s.tenant_id,s.domain_id,s.dnssec_enabled,s.secondary_hostname,s.secondary_address,s.primary_address,s.tsig_key_name
FROM domain_dns_security s JOIN domains d ON d.tenant_id=s.tenant_id AND d.id=s.domain_id
WHERE d.mode='managed' AND d.status='active'
ORDER BY d.fqdn;

-- name: PlatformGetDNSSecurity :one
SELECT d.fqdn,d.tenant_id AS owner_tenant_id,d.id AS registered_domain_id,s.tenant_id,s.domain_id,s.dnssec_enabled,s.secondary_hostname,s.secondary_address,s.primary_address,s.tsig_key_name
FROM domains d LEFT JOIN domain_dns_security s ON d.tenant_id=s.tenant_id AND d.id=s.domain_id
WHERE d.fqdn=$1 AND d.mode='managed' AND d.status='active';

-- name: PlatformSetDNSSecurity :one
INSERT INTO domain_dns_security(tenant_id,domain_id,dnssec_enabled,secondary_hostname,secondary_address,primary_address,tsig_key_name)
SELECT d.tenant_id,d.id,$2,$3,$4,$5,$6 FROM domains d WHERE d.fqdn=$1 AND d.tenant_id=$7 AND d.id=$8 AND d.mode='managed' AND d.status='active'
ON CONFLICT(tenant_id,domain_id) DO UPDATE SET dnssec_enabled=excluded.dnssec_enabled,secondary_hostname=excluded.secondary_hostname,secondary_address=excluded.secondary_address,primary_address=excluded.primary_address,tsig_key_name=excluded.tsig_key_name,updated_at=now()
RETURNING *;
