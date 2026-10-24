-- Platform resource IDs and tenant IDs are different namespaces.
-- name: PlatformResolveAuditTargetTenant :one
SELECT tenant_id FROM (
    SELECT id, id AS tenant_id, 'tenants'::text AS kind FROM tenants
    UNION ALL SELECT id, tenant_id, 'orders'::text FROM orders
    UNION ALL SELECT id, tenant_id, 'invoices'::text FROM invoices
 UNION ALL SELECT id,tenant_id,'refunds'::text FROM refunds
 UNION ALL SELECT id,tenant_id,'payment-reviews'::text FROM payment_manual_reviews
) AS resource
WHERE kind = @kind::text AND id = @id::bigint;
