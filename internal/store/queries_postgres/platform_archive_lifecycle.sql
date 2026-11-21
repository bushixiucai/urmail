-- name: PlatformResolvePinnedArchiveMailbox :one
SELECT m.id, m.tenant_id, m.address FROM mailboxes m
WHERE m.tenant_id= @tenant_id AND m.id= @mailbox_id AND m.address= @address FOR KEY SHARE;

-- name: PlatformIndexArchiveCopy :one
INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,subject,from_addr,to_addr,received_at,size_bytes,raw_sha256)
VALUES(@tenant_id,@mailbox_id,@message_id,@path,@subject,@from_addr,@to_addr,@received_at,@size_bytes,@raw_sha256)
ON CONFLICT ON CONSTRAINT archive_entries_tenant_mailbox_message_uniq
DO UPDATE SET raw_sha256=EXCLUDED.raw_sha256
RETURNING id,path;

-- name: PlatformIndexArchiveContent :exec
INSERT INTO archive_content(entry_id,tenant_id,body_text,attachments,extraction_error,original_sha256,body_html)
VALUES(@entry_id,@tenant_id,@body_text,@attachments,@extraction_error,@original_sha256,@body_html)
ON CONFLICT(entry_id) DO NOTHING;

-- name: PlatformApplyArchiveRetention :many
WITH candidates AS (
 SELECT a.id,a.tenant_id,p.confirmed_preview FROM archive_entries a
 JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
 JOIN retention_policies p ON p.tenant_id=a.tenant_id
 WHERE p.active AND urmail_retention_authorized(p.tenant_id,p.created_by,p.authorization_grant_id) AND p.action='soft_delete' AND p.confirmed_preview IS NOT NULL
 AND NOT a.legal_hold AND a.deleted_at IS NULL
 AND a.received_at < statement_timestamp()-make_interval(days=>p.retention_days)
 ORDER BY a.id FOR UPDATE OF a,p SKIP LOCKED LIMIT 100
), removed AS (
 UPDATE archive_entries a SET deleted_at=statement_timestamp() FROM candidates c
 WHERE a.id=c.id AND a.tenant_id=c.tenant_id RETURNING a.id,a.tenant_id
)
UPDATE archive_content content SET retention_preview=c.confirmed_preview
FROM candidates c JOIN removed r ON r.id=c.id AND r.tenant_id=c.tenant_id
WHERE content.entry_id=c.id AND content.tenant_id=c.tenant_id
RETURNING content.entry_id AS id,content.tenant_id;

-- name: PlatformListArchivePurgeCandidates :many
SELECT a.id,a.tenant_id,a.path,coalesce(nullif(a.raw_sha256,''),c.original_sha256)::text AS raw_sha256,c.purge_started_at,c.purged_at
FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE NOT a.legal_hold AND a.deleted_at<=statement_timestamp()-interval '30 days' AND c.purged_at IS NULL
 AND (c.purge_started_at IS NOT NULL OR c.retention_preview IS NULL OR EXISTS(SELECT 1 FROM retention_policies p WHERE p.tenant_id=a.tenant_id AND p.active AND p.confirmed_preview=c.retention_preview AND urmail_retention_authorized(p.tenant_id,p.created_by,p.authorization_grant_id)))
ORDER BY a.id LIMIT 100;

-- name: PlatformLockArchivePurgeCandidate :one
SELECT a.id,a.path,coalesce(nullif(a.raw_sha256,''),c.original_sha256)::text AS raw_sha256,c.purge_started_at,c.retention_preview
FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE a.tenant_id= @tenant_id AND a.id= @id AND NOT a.legal_hold
 AND a.deleted_at<=statement_timestamp()-interval '30 days' AND c.purged_at IS NULL
FOR UPDATE OF a,c;

-- name: PlatformBeginArchivePurge :exec
UPDATE archive_content SET purge_started_at=coalesce(purge_started_at,statement_timestamp()),purge_error=''
WHERE tenant_id= @tenant_id AND entry_id= @entry_id AND purged_at IS NULL;

-- name: PlatformFinishArchivePurge :exec
UPDATE archive_content SET purged_at=statement_timestamp(),purge_error='',body_text='',body_html='',attachments='[]'
WHERE tenant_id= @tenant_id AND entry_id= @entry_id AND purge_started_at IS NOT NULL;

-- name: PlatformFailArchivePurge :exec
UPDATE archive_content SET purge_error= @purge_error WHERE tenant_id= @tenant_id AND entry_id= @entry_id;

-- name: PlatformListArchiveUnindexedOriginals :many
SELECT a.id,a.tenant_id,a.mailbox_id,a.path,a.raw_sha256
FROM archive_entries a LEFT JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE c.entry_id IS NULL AND a.id > @after_id::bigint
ORDER BY a.id LIMIT 100;

-- name: PlatformSetArchiveOriginalHash :exec
UPDATE archive_entries a SET raw_sha256= @raw_sha256
WHERE a.tenant_id= @tenant_id AND a.id= @id AND a.raw_sha256=''
 AND NOT EXISTS(SELECT 1 FROM archive_entries b WHERE b.tenant_id=a.tenant_id AND b.mailbox_id IS NOT DISTINCT FROM a.mailbox_id AND b.message_id=a.message_id AND b.raw_sha256= @raw_sha256 AND b.id<>a.id);

-- name: PlatformLockRetentionPurgePolicy :one
SELECT coalesce(p.active AND p.confirmed_preview= @preview_id::uuid AND urmail_retention_authorized(p.tenant_id,p.created_by,p.authorization_grant_id),false)::boolean AS allowed
FROM retention_policies p WHERE p.tenant_id= @tenant_id FOR UPDATE;

-- name: PlatformCancelArchivePurgeIntent :exec
UPDATE archive_content SET purge_started_at=NULL,purge_error=''
WHERE tenant_id= @tenant_id AND entry_id= @entry_id AND purged_at IS NULL;

-- name: PlatformArchiveCopyLifecycle :one
SELECT a.legal_hold,a.deleted_at,c.purge_started_at,c.purged_at
FROM archive_entries a LEFT JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE a.tenant_id= @tenant_id AND a.id= @id;
