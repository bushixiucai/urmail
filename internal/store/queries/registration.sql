-- name: CanCreatePendingDomain :one
SELECT EXISTS(SELECT 1 FROM tenant_contacts tc JOIN tenants t ON t.id=tc.tenant_id
WHERE tc.tenant_id=$1 AND t.status='pending' AND NOT EXISTS(SELECT 1 FROM domains d WHERE d.tenant_id=$1)
AND NOT EXISTS(SELECT 1 FROM subscriptions s WHERE s.tenant_id=$1 AND s.status IN('active','trialing','past_due')));

-- name: SelfServicePurchaseAllowed :one
SELECT NOT EXISTS(SELECT 1 FROM tenant_contacts tc WHERE tc.tenant_id=$1)
OR EXISTS(SELECT 1 FROM tenant_contacts tc JOIN domains d ON d.tenant_id=tc.tenant_id
WHERE tc.tenant_id=$1 AND d.id=$2 AND d.status='active' AND d.verified_at IS NOT NULL AND d.verified_at<=now());

-- name: IsSelfServiceTenant :one
SELECT EXISTS(SELECT 1 FROM tenant_contacts WHERE tenant_id=$1);

-- name: ResolvePurchaseDomain :one
SELECT id FROM domains WHERE tenant_id=$1 AND (sqlc.arg(domain_id)::bigint=0 OR id=sqlc.arg(domain_id))
AND status='active' AND verified_at IS NOT NULL AND verified_at<=now() ORDER BY id LIMIT 1;
