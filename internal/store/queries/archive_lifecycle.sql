-- name: GetArchiveContent :one
-- scope: tenant

SELECT a.id,a.tenant_id,a.mailbox_id,a.path,coalesce(nullif(a.raw_sha256,''),c.original_sha256) AS raw_sha256,a.message_id,a.size_bytes,c.body_text,c.body_html,c.attachments,c.extraction_error
FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE a.tenant_id= @tenant_id AND a.id= @id AND a.deleted_at IS NULL AND c.purge_started_at IS NULL;

-- name: SearchArchiveContent :many
-- scope: tenant

SELECT a.id,a.mailbox_id,a.message_id,a.subject,a.from_addr,a.to_addr,a.received_at,a.size_bytes,a.legal_hold,a.deleted_at
FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE a.tenant_id= @tenant_id AND (a.deleted_at IS NOT NULL)= @include_deleted AND c.purge_started_at IS NULL
 AND (@query='' OR to_tsvector('simple',coalesce(a.subject,'')||' '||c.body_text) @@ plainto_tsquery('simple',@query)
 OR coalesce(a.subject,'') LIKE '%'||@query||'%'
 OR c.body_text LIKE '%'||@query||'%')
 AND (sqlc.narg('mailbox_id') IS NULL OR a.mailbox_id=sqlc.narg('mailbox_id'))
 AND (@from_addr='' OR instr(lower(coalesce(a.from_addr,''), lower(@from_addr)))>0)
 AND (sqlc.narg('after_time') IS NULL OR a.received_at>=sqlc.narg('after_time'))
 AND (sqlc.narg('before_time') IS NULL OR a.received_at<sqlc.narg('before_time'))
 AND (sqlc.narg('cursor_time') IS NULL OR (a.received_at,a.id)<(sqlc.narg('cursor_time'),sqlc.narg('cursor_id')))
ORDER BY a.received_at DESC,a.id DESC LIMIT @limit_count;

-- name: PreviewArchiveRetention :one
-- scope: tenant

INSERT INTO retention_previews(tenant_id,retention_days,candidate_count,candidate_digest,created_by,created_at,expires_at)
SELECT @tenant_id,@retention_days,count(*),md5(coalesce(group_concat(id,',' ORDER BY id),'')),@created_by,(CAST(strftime('%s','now') AS INTEGER)*1000000),(CAST(strftime('%s','now') AS INTEGER)*1000000)
FROM archive_entries
WHERE tenant_id= @tenant_id AND NOT legal_hold AND deleted_at IS NULL
 AND received_at<(CAST(strftime('%s','now') AS INTEGER)*1000000)-(0)
RETURNING *;

-- name: LockArchiveRetentionPreview :one
-- scope: tenant

SELECT * FROM retention_previews WHERE tenant_id= @tenant_id AND id= @id AND created_by= @created_by AND confirmed_at IS NULL AND expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: ArchiveRetentionCandidateDigest :one
-- scope: tenant

SELECT count(*) AS candidate_count,md5(coalesce(group_concat(id,',' ORDER BY id),'')) AS candidate_digest
FROM archive_entries WHERE tenant_id= @tenant_id AND NOT legal_hold AND deleted_at IS NULL
 AND received_at < @cutoff;

-- name: ConfirmArchiveRetentionPreview :exec
-- scope: tenant

UPDATE retention_previews SET confirmed_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE tenant_id= @tenant_id AND id= @id AND confirmed_at IS NULL;

-- name: ActivateArchiveRetention :exec
-- scope: tenant

INSERT INTO retention_policies(tenant_id,retention_days,action,active,created_by,confirmed_preview,authorization_grant_id)
VALUES(@tenant_id,@retention_days,'soft_delete',true,@created_by,@confirmed_preview,@authorization_grant_id)
ON CONFLICT(tenant_id) DO UPDATE SET retention_days=EXCLUDED.retention_days,action='soft_delete',active=true,created_by=EXCLUDED.created_by,confirmed_preview=EXCLUDED.confirmed_preview,authorization_grant_id=EXCLUDED.authorization_grant_id,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: RestoreArchiveCopy :execrows
-- scope: tenant

UPDATE archive_entries SET deleted_at=NULL
WHERE tenant_id= @tenant_id AND id= @id AND deleted_at IS NOT NULL
 AND EXISTS(SELECT 1  WHERE EXISTS(SELECT 1 FROM archive_content c WHERE c.entry_id=id AND c.tenant_id=tenant_id AND c.purge_started_at IS NULL AND c.purged_at IS NULL));-- name: GetArchiveOriginalForRecovery :one
-- scope: tenant

SELECT a.id,a.tenant_id,a.mailbox_id,a.path,coalesce(nullif(a.raw_sha256,''),c.original_sha256) AS raw_sha256
FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE a.tenant_id= @tenant_id AND a.id= @id AND a.deleted_at IS NOT NULL AND c.purge_started_at IS NULL AND c.purged_at IS NULL;
