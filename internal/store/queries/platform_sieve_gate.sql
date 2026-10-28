-- Service-only lookup. The principal comes from a kernel-authenticated local
-- Dovecot helper, not from a tenant-controlled HTTP request or message header.
-- name: PlatformSieveOutboundAllowed :one
SELECT EXISTS(SELECT 1 FROM mailboxes m
JOIN tenants t ON t.id=m.tenant_id
JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE m.address=$1 AND m.status='active' AND m.send_enabled
AND t.status='active' AND d.status='active'
AND urmail_domain_mail_ready(m.tenant_id,m.domain_id)
AND urmail_mail_allowed(m.tenant_id))::boolean AS allowed;
