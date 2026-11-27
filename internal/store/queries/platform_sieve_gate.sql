-- Service-only lookup. The principal comes from a kernel-authenticated local
-- Dovecot helper, not from a tenant-controlled HTTP request or message header.
-- name: PlatformSieveOutboundAllowed :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM mailboxes m
JOIN tenants t ON t.id=m.tenant_id
JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE m.address=?1 AND m.status='active' AND m.send_enabled
AND t.status='active' AND d.status='active'
AND EXISTS(SELECT 1 FROM domains rd WHERE rd.tenant_id=m.tenant_id AND rd.id=m.domain_id AND rd.status='active' AND rd.verified_at IS NOT NULL AND rd.provisioning_state='ready')
AND EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=m.tenant_id AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready')))) AS allowed;
