-- Mailbox-user self-service settings, identities and quota (WEBMAIL-005/006).
-- Every statement is bound to the tenant_id and mailbox_id of the verified
-- mailbox bearer session; cross-mailbox rows are unreachable by construction.

-- name: PlatformGetMailboxSettings :one
-- scope: platform

SELECT display_name,signature_text,signature_html,preferences,identity_profiles,version,updated_at
FROM mailbox_settings WHERE tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id);

-- name: PlatformUpdateMailboxSettings :execrows
-- scope: platform

UPDATE mailbox_settings
SET display_name=sqlc.arg(display_name),
    signature_text=sqlc.arg(signature_text),
    signature_html=sqlc.arg(signature_html),
    preferences=sqlc.arg(preferences),
    identity_profiles=COALESCE(sqlc.narg(identity_profiles),identity_profiles),
    version=version+1,
    updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id)
  AND version=sqlc.arg(expected_version);

-- name: PlatformInsertMailboxSettings :execrows
-- scope: platform

INSERT INTO mailbox_settings(tenant_id,mailbox_id,display_name,signature_text,signature_html,preferences,identity_profiles)
VALUES(sqlc.arg(tenant_id),sqlc.arg(mailbox_id),sqlc.arg(display_name),sqlc.arg(signature_text),sqlc.arg(signature_html),sqlc.arg(preferences),COALESCE(sqlc.narg(identity_profiles),'{}'))
ON CONFLICT(mailbox_id) DO NOTHING;

-- name: PlatformGetMailboxQuota :one
-- scope: platform

SELECT m.bytes_used,m.quota_bytes FROM mailboxes m
WHERE m.tenant_id=sqlc.arg(tenant_id) AND m.id=sqlc.arg(mailbox_id) AND m.status='active';

-- name: PlatformListMailboxSenderIdentities :many
-- scope: platform

SELECT DISTINCT a.local_part,d.fqdn FROM mailbox_aliases a
JOIN domains d ON d.id=a.domain_id AND d.tenant_id=a.tenant_id
WHERE a.tenant_id=sqlc.arg(tenant_id) AND a.active
  AND d.status='active' AND d.provisioning_state='ready'
  AND EXISTS(SELECT 1 FROM json_each(a.sender_logins) je WHERE je.value = sqlc.arg(sender))
ORDER BY a.local_part,d.fqdn;

-- name: PlatformMailboxSenderIdentityAllowed :execrows
-- scope: platform

SELECT 1 FROM mailbox_aliases a
JOIN domains d ON d.id=a.domain_id AND d.tenant_id=a.tenant_id
WHERE a.tenant_id=sqlc.arg(tenant_id) AND a.active
  AND d.status='active' AND d.provisioning_state='ready'
  AND EXISTS(SELECT 1 FROM json_each(a.sender_logins) je WHERE je.value = sqlc.arg(sender))
  AND sqlc.arg(from_address)=a.local_part||'@'||d.fqdn
LIMIT 1;
