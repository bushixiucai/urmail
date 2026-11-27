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

-- PlatformApplyArchiveRetentionCandidates 在 S7 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- archiveRetentionCandidatesSQL 保留为 Go 常量，供 MarkContent/MarkDeleted 复合拼接。
-- PlatformApplyArchiveRetentionMarkDeleted 在 S7 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- 修正后 IN 子查询为 2 列（archiveRetentionCandidateKeysSQL），不再与 3 列 SELECT 列数不匹配。
-- PlatformApplyArchiveRetentionMarkContent 在 S7 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- 修正后 IN 子查询为 2 列；retention_preview 由 SET 标量子查询单独提供。
-- name: PlatformListArchivePurgeCandidates :many
-- scope: platform

SELECT a.id,a.tenant_id,a.path,coalesce(nullif(a.raw_sha256,''),c.original_sha256) AS raw_sha256,c.purge_started_at,c.purged_at
FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE NOT a.legal_hold AND a.deleted_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000)-2592000000000 AND c.purged_at IS NULL
 AND (c.purge_started_at IS NOT NULL OR c.retention_preview IS NULL OR EXISTS(SELECT 1 FROM retention_policies p WHERE p.tenant_id=a.tenant_id AND p.active AND p.confirmed_preview=c.retention_preview AND EXISTS(SELECT 1 FROM compliance_grants g JOIN user_tenant_roles r ON r.tenant_id=g.tenant_id AND r.user_id=g.user_id
  WHERE g.tenant_id=p.tenant_id AND g.id=p.authorization_grant_id AND g.user_id=p.created_by AND g.scope='retention_manage'
   AND NOT g.revoked AND g.owner_confirmed_at IS NOT NULL AND g.owner_confirmed_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000) AND g.granted_by IS NOT NULL
   AND length(trim(g.reason))>0 AND g.expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND r.role IN('owner','admin'))))
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

SELECT coalesce(p.active AND p.confirmed_preview= @preview_id AND EXISTS(SELECT 1 FROM compliance_grants g JOIN user_tenant_roles r ON r.tenant_id=g.tenant_id AND r.user_id=g.user_id
  WHERE g.tenant_id=p.tenant_id AND g.id=p.authorization_grant_id AND g.user_id=p.created_by AND g.scope='retention_manage'
   AND NOT g.revoked AND g.owner_confirmed_at IS NOT NULL AND g.owner_confirmed_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000) AND g.granted_by IS NOT NULL
   AND length(trim(g.reason))>0 AND g.expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND r.role IN('owner','admin')),false) AS allowed
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
