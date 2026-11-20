-- name: GetArchiveContent :one
-- scope: tenant

SELECT a.id,a.tenant_id,a.mailbox_id,a.path,coalesce(nullif(a.raw_sha256,''),c.original_sha256) AS raw_sha256,a.message_id,a.size_bytes,c.body_text,c.body_html,c.attachments,c.extraction_error
FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE a.tenant_id= @tenant_id AND a.id= @id AND a.deleted_at IS NULL AND c.purge_started_at IS NULL;

-- name: SearchArchiveContent :many
-- scope: tenant

SELECT a.id, a.mailbox_id, a.message_id, a.subject, a.from_addr, a.to_addr, a.received_at, a.size_bytes, a.legal_hold, coalesce(a.deleted_at, 0) AS deleted_at
FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE a.tenant_id= @tenant_id AND (a.deleted_at IS NOT NULL)=CAST(@include_deleted AS BOOLEAN) AND c.purge_started_at IS NULL
 AND (CAST(@query AS TEXT)='' OR a.subject LIKE '%' || CAST(@query AS TEXT) || '%' OR c.body_text LIKE '%' || CAST(@query AS TEXT) || '%'
 OR coalesce(a.subject,'') LIKE '%'||CAST(@query AS TEXT)||'%'
 OR c.body_text LIKE '%'||CAST(@query AS TEXT)||'%')
 AND (CAST(sqlc.narg('mailbox_id') AS INTEGER) IS NULL OR a.mailbox_id=CAST(sqlc.narg('mailbox_id') AS INTEGER))
 AND (CAST(@from_addr AS TEXT)='' OR instr(lower(coalesce(a.from_addr,'')), lower(CAST(@from_addr AS TEXT)))>0)
 AND (CAST(sqlc.narg('after_time') AS INTEGER) IS NULL OR a.received_at>=CAST(sqlc.narg('after_time') AS INTEGER))
 AND (CAST(sqlc.narg('before_time') AS INTEGER) IS NULL OR a.received_at<CAST(sqlc.narg('before_time') AS INTEGER))
 AND (CAST(sqlc.narg('cursor_time') AS INTEGER) IS NULL OR (a.received_at,a.id)<(CAST(sqlc.narg('cursor_time') AS INTEGER),CAST(sqlc.narg('cursor_id') AS INTEGER)))
ORDER BY a.received_at DESC,a.id DESC LIMIT CAST(@limit_count AS INTEGER);

-- name: PreviewArchiveRetention :one
-- scope: tenant

-- The candidate set and its digest must be the same set: the previous port left
-- `-(0)` where make_interval(days=>retention_days) was, and the digest subquery
-- had no WHERE (whole-table, cross-tenant). Both are restored here from the PG
-- the UUID default the PG DDL carried (gen_random_uuid is not expressible in
-- the generated SQLite schema); it is a v4-shaped lowercase UUID text.
INSERT INTO retention_previews(id,tenant_id,retention_days,candidate_count,candidate_digest,created_by,created_at,expires_at)
SELECT lower(hex(randomblob(4))||'-'||hex(randomblob(2))||'-4'||substr(hex(randomblob(2)),2)||'-'||substr('89ab',1+(abs(random())%4),1)||substr(hex(randomblob(2)),2)||'-'||hex(randomblob(6))),
 @tenant_id,@retention_days,count(*),
 md5(coalesce((SELECT group_concat(id, ',') FROM (
   SELECT a.id FROM archive_entries a
   WHERE a.tenant_id=@tenant_id AND NOT a.legal_hold AND a.deleted_at IS NULL
    AND a.received_at<(CAST(strftime('%s','now') AS INTEGER)*1000000)-(@retention_days*86400000000)
   ORDER BY a.id)),'')),
 @created_by,(CAST(strftime('%s','now') AS INTEGER)*1000000),(CAST(strftime('%s','now') AS INTEGER)*1000000)+900000000000
FROM archive_entries
WHERE archive_entries.tenant_id= @tenant_id AND NOT archive_entries.legal_hold AND archive_entries.deleted_at IS NULL
 AND archive_entries.received_at<(CAST(strftime('%s','now') AS INTEGER)*1000000)-(@retention_days*86400000000)
RETURNING id, tenant_id, retention_days, candidate_count, candidate_digest, created_by, created_at, coalesce(expires_at, 0) AS expires_at, coalesce(confirmed_at, 0) AS confirmed_at, authorization_grant_id;

-- name: LockArchiveRetentionPreview :one
-- scope: tenant

SELECT id, tenant_id, retention_days, candidate_count, candidate_digest, created_by, created_at, coalesce(expires_at, 0) AS expires_at, coalesce(confirmed_at, 0) AS confirmed_at, authorization_grant_id FROM retention_previews WHERE tenant_id= @tenant_id AND id= @id AND created_by= @created_by AND confirmed_at IS NULL AND expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: ArchiveRetentionCandidateDigest :one
-- scope: tenant

-- Same WHERE as PreviewArchiveRetention (tenant-scoped candidate set), so the
-- confirmation evidence and the preview describe the same rows. The previous
-- port hashed every tenant's ids and ignored the cutoff.
SELECT count(*) AS candidate_count,
 md5(coalesce((SELECT group_concat(id, ',') FROM (
   SELECT a.id FROM archive_entries a
   WHERE a.tenant_id=@tenant_id AND NOT a.legal_hold AND a.deleted_at IS NULL
    AND a.received_at < @cutoff
   ORDER BY a.id)),'')) AS candidate_digest
FROM archive_entries WHERE archive_entries.tenant_id= @tenant_id AND NOT archive_entries.legal_hold AND archive_entries.deleted_at IS NULL
 AND archive_entries.received_at < @cutoff;

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
WHERE archive_entries.tenant_id= @tenant_id AND archive_entries.id= @id AND archive_entries.deleted_at IS NOT NULL
 AND EXISTS(SELECT 1 FROM archive_content c WHERE c.entry_id=archive_entries.id AND c.tenant_id=archive_entries.tenant_id AND c.purge_started_at IS NULL AND c.purged_at IS NULL);
-- name: GetArchiveOriginalForRecovery :one
-- scope: tenant

SELECT a.id,a.tenant_id,a.mailbox_id,a.path,coalesce(nullif(a.raw_sha256,''),c.original_sha256) AS raw_sha256
FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id
WHERE a.tenant_id= @tenant_id AND a.id= @id AND a.deleted_at IS NOT NULL AND c.purge_started_at IS NULL AND c.purged_at IS NULL;

-- 中文注记（sqlc 的 sqlite 引擎按字节偏移切分语句，任何非 ASCII 注记都必须放在文件末尾）：
-- PreviewArchiveRetention: definition in PHASE1-S2-PG-BUILTIN-FUNCS-AUDIT-20261120.md §D. `id` supplies
