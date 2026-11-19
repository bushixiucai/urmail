-- Platform resource IDs and tenant IDs are different namespaces.
-- name: PlatformResolveAuditTargetTenant :one
-- scope: platform

SELECT tenant_id FROM (
    SELECT id, id AS tenant_id, 'tenants' AS kind FROM tenants
) AS resource
WHERE kind = @kind AND id = @id;
