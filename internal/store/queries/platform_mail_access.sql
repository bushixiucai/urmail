-- name: PlatformMailboxAccessStates :many
-- scope: platform

SELECT m.tenant_id,m.id AS mailbox_id,m.address,m.password_version,m.password_revoke_pending, (EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=m.tenant_id AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready'))) AND m.status='active' AND t.status='active' AND d.status='active') AS allowed
FROM mailboxes m JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.id=m.domain_id AND d.tenant_id=m.tenant_id
ORDER BY m.address;

-- name: PlatformNextMailAccessBoundary :one
-- scope: platform

-- Sending no longer expires with a subscription. There is no planned boundary.
SELECT NULL AS next_boundary;
