-- name: PlatformRestoreSyncEpoch :one
-- scope: platform

SELECT COALESCE((SELECT CAST(json_extract(value,'$.sync_epoch') AS INTEGER) FROM settings
 WHERE scope='platform' AND scope_id=0 AND key='restore_replay_review'),1) AS epoch;

-- name: PlatformMailboxRestoreIdentity :one
-- scope: platform

SELECT m.tenant_id,m.id,m.address,m.local_part,d.fqdn,m.quota_bytes
FROM mailboxes m JOIN domains d ON d.id=m.domain_id AND d.tenant_id=m.tenant_id
JOIN mailbox_storage_reservations r ON r.mailbox_id=m.id AND r.address=m.address
WHERE m.tenant_id=?1 AND m.id=?2 AND m.status='active';
