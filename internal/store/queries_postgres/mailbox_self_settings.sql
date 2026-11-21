-- Mailbox-user self-service settings, identities and quota (WEBMAIL-005/006).
-- Every statement is bound to the tenant_id and mailbox_id of the verified
-- mailbox bearer session; cross-mailbox rows are unreachable by construction.

-- name: PlatformGetMailboxSettings :one
SELECT display_name,signature_text,signature_html,preferences,identity_profiles,version,updated_at
FROM mailbox_settings WHERE tenant_id=sqlc.arg(tenant_id)::bigint AND mailbox_id=sqlc.arg(mailbox_id)::bigint;

-- name: PlatformUpdateMailboxSettings :execrows
UPDATE mailbox_settings
SET display_name=sqlc.arg(display_name)::text,
    signature_text=sqlc.arg(signature_text)::text,
    signature_html=sqlc.arg(signature_html)::text,
    preferences=sqlc.arg(preferences)::jsonb,
    identity_profiles=COALESCE(sqlc.narg(identity_profiles)::jsonb,identity_profiles),
    version=version+1,
    updated_at=now()
WHERE tenant_id=sqlc.arg(tenant_id)::bigint AND mailbox_id=sqlc.arg(mailbox_id)::bigint
  AND version=sqlc.arg(expected_version)::integer;

-- name: PlatformInsertMailboxSettings :execrows
INSERT INTO mailbox_settings(tenant_id,mailbox_id,display_name,signature_text,signature_html,preferences,identity_profiles)
VALUES(sqlc.arg(tenant_id)::bigint,sqlc.arg(mailbox_id)::bigint,sqlc.arg(display_name)::text,sqlc.arg(signature_text)::text,sqlc.arg(signature_html)::text,sqlc.arg(preferences)::jsonb,COALESCE(sqlc.narg(identity_profiles)::jsonb,'{}'::jsonb))
ON CONFLICT(mailbox_id) DO NOTHING;

-- name: PlatformGetMailboxQuota :one
SELECT m.bytes_used,m.quota_bytes FROM mailboxes m
WHERE m.tenant_id=sqlc.arg(tenant_id)::bigint AND m.id=sqlc.arg(mailbox_id)::bigint AND m.status='active';

-- name: PlatformListMailboxSenderIdentities :many
SELECT DISTINCT a.local_part,d.fqdn FROM mailbox_aliases a
JOIN domains d ON d.id=a.domain_id AND d.tenant_id=a.tenant_id
WHERE a.tenant_id=sqlc.arg(tenant_id)::bigint AND a.active
  AND d.status='active' AND d.provisioning_state='ready'
  AND sqlc.arg(sender)::text=ANY(a.sender_logins)
ORDER BY a.local_part,d.fqdn;

-- name: PlatformMailboxSenderIdentityAllowed :execrows
SELECT 1 FROM mailbox_aliases a
JOIN domains d ON d.id=a.domain_id AND d.tenant_id=a.tenant_id
WHERE a.tenant_id=sqlc.arg(tenant_id)::bigint AND a.active
  AND d.status='active' AND d.provisioning_state='ready'
  AND sqlc.arg(sender)::text=ANY(a.sender_logins)
  AND sqlc.arg(from_address)::text=a.local_part||'@'||d.fqdn
LIMIT 1;
