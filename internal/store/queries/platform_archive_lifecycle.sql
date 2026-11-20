-- name: PlatformResolvePinnedArchiveMailbox :one
-- scope: platform

SELECT m.id, m.tenant_id, m.address FROM mailboxes m
WHERE m.tenant_id= @tenant_id AND m.id= @mailbox_id AND m.address= @address FOR KEY SHARE;

-- name: PlatformIndexArchiveCopy :one
-- scope: platform

INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,subject,from_addr,to_addr,received_at,size_bytes,raw_sha256)
VALUES(@tenant_id,@mailbox_id,@message_id,@path,@subject,@from_addr,@to_addr,@received_at,@size_bytes,@raw_sha256)
ON CONFLICT ON CONSTRAINT archive_entries_tenant_mailbox_message_uniq
DO UPDATE SET raw_sha256=EXCLUDED.raw_sha256
RETURNING id,path;

-- name: PlatformIndexArchiveContent :exec
-- scope: platform

INSERT INTO archive_content(entry_id,tenant_id,body_text,attachments,extraction_error,original_sha256,body_html)
VALUES(@entry_id,@tenant_id,@body_text,@attachments,@extraction_error,@original_sha256,@body_html)
ON CONFLICT(entry_id) DO NOTHING;

-- name: PlatformApplyArchiveRetentionCandidates :many
-- scope: platform

SELECT a.id, a.tenant_id, p.confirmed_preview FROM archive_entries a
JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
JOIN retention_policies p ON p.tenant_id=a.tenant_id
WHERE p.active AND p.action='soft_delete' AND p.confirmed_preview IS NOT NULL
 AND (SELECT COALESCE((SELECT 1 FROM compliance_grants cg JOIN user_tenant_roles ur ON ur.tenant_id=cg.tenant_id AND ur.user_id=cg.user_id
      WHERE cg.tenant_id=p.tenant_id AND cg.user_id=p.created_by AND cg.scope='retention_manage' AND NOT cg.revoked
       AND cg.owner_confirmed_at IS NOT NULL AND ur.role IN('owner','admin')), 0))=1
 AND NOT a.legal_hold AND a.deleted_at IS NULL
 AND a.received_at < (CAST(strftime('%s','now') AS INTEGER)*1000000) - p.retention_days*86400000000
ORDER BY a.id LIMIT 100;

-- name: PlatformApplyArchiveRetentionMarkDeleted :execrows
-- scope: platform

UPDATE archive_entries SET deleted_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE (id, tenant_id) IN (SELECT a.id, a.tenant_id, p.confirmed_preview FROM archive_entries a
 JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
 JOIN retention_policies p ON p.tenant_id=a.tenant_id
 WHERE p.active AND p.action='soft_delete' AND p.confirmed_preview IS NOT NULL
  AND (SELECT COALESCE((SELECT 1 FROM compliance_grants cg JOIN user_tenant_roles ur ON ur.tenant_id=cg.tenant_id AND ur.user_id=cg.user_id
       WHERE cg.tenant_id=p.tenant_id AND cg.user_id=p.created_by AND cg.scope='retention_manage' AND NOT cg.revoked
        AND cg.owner_confirmed_at IS NOT NULL AND ur.role IN('owner','admin')), 0))=1
  AND NOT a.legal_hold AND a.deleted_at IS NULL
  AND a.received_at < (CAST(strftime('%s','now') AS INTEGER)*1000000) - p.retention_days*86400000000
 ORDER BY a.id LIMIT 100);

-- name: PlatformApplyArchiveRetentionMarkContent :exec
-- scope: platform

UPDATE archive_content SET retention_preview=(
 SELECT confirmed_preview FROM retention_policies p WHERE p.tenant_id=archive_content.tenant_id AND p.active AND p.action='soft_delete')
WHERE (entry_id, tenant_id) IN (SELECT a.id, a.tenant_id, p.confirmed_preview FROM archive_entries a
 JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
 JOIN retention_policies p ON p.tenant_id=a.tenant_id
 WHERE p.active AND p.action='soft_delete' AND p.confirmed_preview IS NOT NULL
  AND (SELECT COALESCE((SELECT 1 FROM compliance_grants cg JOIN user_tenant_roles ur ON ur.tenant_id=cg.tenant_id AND ur.user_id=cg.user_id
       WHERE cg.tenant_id=p.tenant_id AND cg.user_id=p.created_by AND cg.scope='retention_manage' AND NOT cg.revoked
        AND cg.owner_confirmed_at IS NOT NULL AND ur.role IN('owner','admin')), 0))=1
  AND NOT a.legal_hold AND a.deleted_at IS NULL
  AND a.received_at < (CAST(strftime('%s','now') AS INTEGER)*1000000) - p.retention_days*86400000000
 ORDER BY a.id LIMIT 100);
-- name: PlatformListArchivePurgeCandidates :many
-- scope: platform

SELECT a.id,a.tenant_id,a.path,coalesce(nullif(a.raw_sha256,''),c.original_sha256) AS raw_sha256,c.purge_started_at,c.purged_at
FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE NOT a.legal_hold AND a.deleted_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000)-2592000000000 AND c.purged_at IS NULL
 AND (c.purge_started_at IS NOT NULL OR c.retention_preview IS NULL OR EXISTS(SELECT 1 FROM retention_policies p WHERE p.tenant_id=a.tenant_id AND p.active AND p.confirmed_preview=c.retention_preview AND urmail_retention_authorized(p.tenant_id,p.created_by,p.authorization_grant_id)))
ORDER BY a.id LIMIT 100;

-- name: PlatformLockArchivePurgeCandidate :one
-- scope: platform

SELECT a.id,a.path,coalesce(nullif(a.raw_sha256,''),c.original_sha256) AS raw_sha256,c.purge_started_at,c.retention_preview
FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE a.tenant_id= @tenant_id AND a.id= @id AND NOT a.legal_hold
 AND a.deleted_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000)-2592000000000 AND c.purged_at IS NULL;

-- name: PlatformBeginArchivePurge :exec
-- scope: platform

UPDATE archive_content SET purge_started_at=coalesce(purge_started_at,(CAST(strftime('%s','now') AS INTEGER)*1000000)),purge_error=''
WHERE tenant_id= @tenant_id AND entry_id= @entry_id AND purged_at IS NULL;

-- name: PlatformFinishArchivePurge :exec
-- scope: platform

UPDATE archive_content SET purged_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),purge_error='',body_text='',body_html='',attachments='[]'
WHERE tenant_id= @tenant_id AND entry_id= @entry_id AND purge_started_at IS NOT NULL;

-- name: PlatformFailArchivePurge :exec
-- scope: platform

UPDATE archive_content SET purge_error= @purge_error WHERE tenant_id= @tenant_id AND entry_id= @entry_id;

-- name: PlatformListArchiveUnindexedOriginals :many
-- scope: platform

SELECT a.id,a.tenant_id,a.mailbox_id,a.path,a.raw_sha256
FROM archive_entries a LEFT JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE c.entry_id IS NULL AND a.id > @after_id
ORDER BY a.id LIMIT 100;

-- name: PlatformSetArchiveOriginalHash :exec
-- scope: platform

UPDATE archive_entries SET raw_sha256= @raw_sha256
WHERE tenant_id= @tenant_id AND id= @id AND raw_sha256=''
 AND NOT EXISTS(SELECT 1  WHERE EXISTS(SELECT 1 FROM archive_entries b WHERE b.tenant_id=tenant_id AND b.mailbox_id IS NOT DISTINCT FROM mailbox_id AND b.message_id=message_id AND b.raw_sha256= @raw_sha256 AND b.id<>id));
-- name: PlatformLockRetentionPurgePolicy :one
-- scope: platform

SELECT coalesce(p.active AND p.confirmed_preview= @preview_id AND urmail_retention_authorized(p.tenant_id,p.created_by,p.authorization_grant_id),false) AS allowed
FROM retention_policies p WHERE p.tenant_id= @tenant_id;

-- name: PlatformCancelArchivePurgeIntent :exec
-- scope: platform

UPDATE archive_content SET purge_started_at=NULL,purge_error=''
WHERE tenant_id= @tenant_id AND entry_id= @entry_id AND purged_at IS NULL;

-- name: PlatformArchiveCopyLifecycle :one
-- scope: platform

SELECT a.legal_hold,a.deleted_at,c.purge_started_at,c.purged_at
FROM archive_entries a LEFT JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE a.tenant_id= @tenant_id AND a.id= @id;
