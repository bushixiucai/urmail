-- name: LockMailboxImportOwner :one
-- scope: tenant

SELECT id,bytes_used,quota_bytes FROM mailboxes
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(mailbox_id) AND status='active';

-- name: MailboxImportStorageBudget :one
-- scope: tenant

SELECT count(CASE WHEN j.mailbox_id=sqlc.arg(mailbox_id) THEN 1 END) AS mailbox_jobs,
 CAST(COALESCE(sum(CASE WHEN j.mailbox_id=sqlc.arg(mailbox_id) THEN j.source_bytes ELSE 0 END),0) AS INTEGER) AS mailbox_bytes,
 CAST(COALESCE(sum(j.source_bytes),0) AS INTEGER) AS tenant_bytes
FROM mailbox_import_jobs j WHERE j.tenant_id=sqlc.arg(tenant_id) AND j.payload_expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000)
AND (j.status NOT IN('completed','canceled') OR EXISTS(SELECT 1 FROM mailbox_import_items i
 WHERE i.tenant_id=j.tenant_id AND i.mailbox_id=j.mailbox_id AND i.job_id=j.id AND i.raw_secret<>''));

-- name: ClearExpiredMailboxImportPayloads :exec
-- scope: tenant

UPDATE mailbox_import_items SET raw_secret=''  WHERE mailbox_import_items.tenant_id=sqlc.arg(tenant_id) AND EXISTS(SELECT 1 FROM mailbox_import_jobs j WHERE j.tenant_id=mailbox_import_items.tenant_id AND j.mailbox_id=mailbox_import_items.mailbox_id AND j.id=mailbox_import_items.job_id AND j.payload_expires_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000));
-- name: CreateMailboxImportJob :one
-- scope: tenant

INSERT INTO mailbox_import_jobs(tenant_id,mailbox_id,request_key,format,filename,folder,uid_validity,source_digest,preview_digest,duplicate_policy,source_bytes)
VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11) RETURNING *;

-- name: CreateMailboxImportItem :exec
-- scope: tenant

INSERT INTO mailbox_import_items(tenant_id,mailbox_id,job_id,item_index,subject,message_id,raw_sha256,raw_bytes,raw_secret,status,error_code,duplicate_of,baseline_uids)
VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13);

-- name: GetMailboxImportByKey :one
-- scope: tenant

SELECT * FROM mailbox_import_jobs WHERE tenant_id=?1 AND mailbox_id=?2 AND request_key=?3;

-- name: GetMailboxImportJob :one
-- scope: tenant

SELECT * FROM mailbox_import_jobs WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3;

-- name: LockMailboxImportJob :one
-- scope: tenant

SELECT * FROM mailbox_import_jobs WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3;

-- name: ListMailboxImportJobs :many
-- scope: tenant

SELECT * FROM mailbox_import_jobs WHERE tenant_id=?1 AND mailbox_id=?2 ORDER BY id DESC LIMIT 20;

-- name: ListMailboxImportItems :many
-- scope: tenant

SELECT * FROM mailbox_import_items WHERE tenant_id=?1 AND mailbox_id=?2 AND job_id=?3 ORDER BY item_index;

-- name: SetMailboxImportJob :one
-- scope: tenant

UPDATE mailbox_import_jobs SET status=sqlc.arg(status),version=version+1,
 confirmed_at=CASE WHEN CAST(sqlc.arg(confirm) AS BOOLEAN) THEN COALESCE(confirmed_at,(CAST(strftime('%s','now') AS INTEGER)*1000000)) ELSE confirmed_at END,
 cancel_requested=sqlc.arg(cancel_requested),lease_token=sqlc.arg(lease_token),lease_until=sqlc.narg(lease_until),updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(job_id)
AND version=sqlc.arg(expected_version) RETURNING *;

-- name: SetMailboxImportItem :execrows
-- scope: tenant

UPDATE mailbox_import_items SET selected=sqlc.arg(selected),status=sqlc.arg(status),error_code=sqlc.arg(error_code),
 baseline_uids=sqlc.arg(baseline_uids),destination_uid=sqlc.narg(destination_uid),
 attempts=attempts+CASE WHEN CAST(sqlc.arg(start_attempt) AS BOOLEAN) THEN 1 ELSE 0 END,
 attempt_started_at=CASE WHEN CAST(sqlc.arg(start_attempt) AS BOOLEAN) THEN (CAST(strftime('%s','now') AS INTEGER)*1000000) ELSE attempt_started_at END,
 reconciled_at=CASE WHEN CAST(sqlc.arg(reconciled) AS BOOLEAN) THEN (CAST(strftime('%s','now') AS INTEGER)*1000000) ELSE reconciled_at END,
 reconcile_absent=sqlc.arg(reconcile_absent),review_reason=sqlc.arg(review_reason),
 raw_secret=CASE WHEN CAST(sqlc.arg(clear_payload) AS BOOLEAN) THEN '' ELSE raw_secret END,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE mailbox_import_items.tenant_id=sqlc.arg(tenant_id) AND mailbox_import_items.mailbox_id=sqlc.arg(mailbox_id) AND mailbox_import_items.job_id=sqlc.arg(job_id) AND mailbox_import_items.item_index=sqlc.arg(item_index)
AND (CAST(sqlc.arg(lease_fence) AS TEXT)='' OR EXISTS(SELECT 1 FROM mailbox_import_jobs j WHERE j.tenant_id=sqlc.arg(tenant_id) AND j.mailbox_id=sqlc.arg(mailbox_id) AND j.id=sqlc.arg(job_id) AND j.lease_token=CAST(sqlc.arg(lease_fence) AS TEXT)));

-- name: MailboxImportReservedBytes :one
-- scope: tenant

SELECT CAST(COALESCE(sum(i.raw_bytes),0) AS INTEGER) FROM mailbox_import_items i
JOIN mailbox_import_jobs j ON j.tenant_id=i.tenant_id AND j.mailbox_id=i.mailbox_id AND j.id=i.job_id
WHERE i.tenant_id=?1 AND i.mailbox_id=?2 AND j.id<>?3 AND j.status NOT IN('completed','canceled')
AND i.status IN('pending','importing','unknown');

-- name: MailboxImportRecoverExpiredLeases :exec
-- scope: tenant

UPDATE mailbox_import_items SET status='unknown',error_code='MAIL_IMPORT_ACK_UNKNOWN',reconciled_at=NULL,reconcile_absent=false,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
 WHERE mailbox_import_items.tenant_id=?1 AND mailbox_import_items.mailbox_id=?2 AND status='importing' AND EXISTS(SELECT 1 FROM mailbox_import_jobs j WHERE j.tenant_id=mailbox_import_items.tenant_id AND j.mailbox_id=mailbox_import_items.mailbox_id AND j.id=mailbox_import_items.job_id AND (j.lease_until IS NULL OR j.lease_until<=(CAST(strftime('%s','now') AS INTEGER)*1000000)));
-- name: RecordMailboxImportAudit :exec
-- scope: tenant

INSERT INTO audit_logs(tenant_id,action,target_id,detail)
SELECT j.tenant_id,sqlc.arg(action),j.id,json_object('mailbox_id',j.mailbox_id,'version',j.version,'detail',sqlc.arg(detail))
FROM mailbox_import_jobs j WHERE j.tenant_id=sqlc.arg(tenant_id) AND j.mailbox_id=sqlc.arg(mailbox_id) AND j.id=sqlc.arg(job_id);

-- name: MailboxImportItemViews :one
-- scope: tenant

SELECT CAST(COALESCE(json_group_array(json_extract(i, '$.raw_secret')),'[]') AS BLOB) AS items
FROM mailbox_import_items i WHERE tenant_id=?1 AND mailbox_id=?2 AND job_id=?3;

-- 中文注记（sqlc 的 sqlite 引擎按字节偏移切分语句，任何非 ASCII 注记都必须放在文件末尾）：
-- MailboxImportItemViews: LockMailboxImportTenantBudget 在 S5 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- MailboxImportItemViews: PG 用事务级 advisory lock 串行化同一租户的导入预算；SQLite 改为单写者事务串行
-- MailboxImportItemViews: （BEGIN IMMEDIATE），详见 scripts/sqlite/lock-semantics.md。
