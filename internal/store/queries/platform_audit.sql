-- Platform resource IDs and tenant IDs are different namespaces.
-- name: PlatformResolveAuditTargetTenant :one
SELECT tenant_id FROM (
    SELECT id, id AS tenant_id, 'tenants'::text AS kind FROM tenants
) AS resource
WHERE kind = @kind::text AND id = @id::bigint;
