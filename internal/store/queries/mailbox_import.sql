-- name: LockMailboxImportOwner :one
SELECT id,bytes_used,quota_bytes FROM mailboxes
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(mailbox_id) AND status='active'
FOR UPDATE;

-- name: MailboxImportStorageBudget :one
SELECT count(*) FILTER(WHERE j.mailbox_id=sqlc.arg(mailbox_id))::bigint AS mailbox_jobs,
 COALESCE(sum(j.source_bytes)FILTER(WHERE j.mailbox_id=sqlc.arg(mailbox_id)),0)::bigint AS mailbox_bytes,
 COALESCE(sum(j.source_bytes),0)::bigint AS tenant_bytes
FROM mailbox_import_jobs j WHERE j.tenant_id=sqlc.arg(tenant_id) AND j.payload_expires_at>now()
AND (j.status NOT IN('completed','canceled') OR EXISTS(SELECT 1 FROM mailbox_import_items i
 WHERE i.tenant_id=j.tenant_id AND i.mailbox_id=j.mailbox_id AND i.job_id=j.id AND i.raw_secret<>''));

-- name: ClearExpiredMailboxImportPayloads :exec
UPDATE mailbox_import_items i SET raw_secret='' FROM mailbox_import_jobs j
WHERE i.tenant_id=sqlc.arg(tenant_id) AND j.tenant_id=i.tenant_id AND j.mailbox_id=i.mailbox_id AND j.id=i.job_id
AND j.payload_expires_at<=now();

-- name: CreateMailboxImportJob :one
INSERT INTO mailbox_import_jobs(tenant_id,mailbox_id,request_key,format,filename,folder,uid_validity,source_digest,preview_digest,duplicate_policy,source_bytes)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING *;

-- name: CreateMailboxImportItem :exec
INSERT INTO mailbox_import_items(tenant_id,mailbox_id,job_id,item_index,subject,message_id,raw_sha256,raw_bytes,raw_secret,status,error_code,duplicate_of,baseline_uids)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13);

-- name: GetMailboxImportByKey :one
SELECT * FROM mailbox_import_jobs WHERE tenant_id=$1 AND mailbox_id=$2 AND request_key=$3;

-- name: GetMailboxImportJob :one
SELECT * FROM mailbox_import_jobs WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3;

-- name: LockMailboxImportJob :one
SELECT * FROM mailbox_import_jobs WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3 FOR UPDATE;

-- name: ListMailboxImportJobs :many
SELECT * FROM mailbox_import_jobs WHERE tenant_id=$1 AND mailbox_id=$2 ORDER BY id DESC LIMIT 20;

-- name: ListMailboxImportItems :many
SELECT * FROM mailbox_import_items WHERE tenant_id=$1 AND mailbox_id=$2 AND job_id=$3 ORDER BY item_index;

-- name: SetMailboxImportJob :one
UPDATE mailbox_import_jobs SET status=sqlc.arg(status),version=version+1,
 confirmed_at=CASE WHEN sqlc.arg(confirm)::boolean THEN COALESCE(confirmed_at,now()) ELSE confirmed_at END,
 cancel_requested=sqlc.arg(cancel_requested),lease_token=sqlc.arg(lease_token),lease_until=sqlc.narg(lease_until),updated_at=now()
WHERE tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(job_id)
AND version=sqlc.arg(expected_version) RETURNING *;

-- name: SetMailboxImportItem :execrows
UPDATE mailbox_import_items AS i SET selected=sqlc.arg(selected),status=sqlc.arg(status),error_code=sqlc.arg(error_code),
 baseline_uids=sqlc.arg(baseline_uids)::bigint[],destination_uid=sqlc.narg(destination_uid),
 attempts=attempts+CASE WHEN sqlc.arg(start_attempt)::boolean THEN 1 ELSE 0 END,
 attempt_started_at=CASE WHEN sqlc.arg(start_attempt)::boolean THEN now() ELSE attempt_started_at END,
 reconciled_at=CASE WHEN sqlc.arg(reconciled)::boolean THEN now() ELSE reconciled_at END,
 reconcile_absent=sqlc.arg(reconcile_absent),review_reason=sqlc.arg(review_reason),
 raw_secret=CASE WHEN sqlc.arg(clear_payload)::boolean THEN '' ELSE raw_secret END,updated_at=now()
WHERE i.tenant_id=sqlc.arg(tenant_id) AND i.mailbox_id=sqlc.arg(mailbox_id) AND i.job_id=sqlc.arg(job_id) AND i.item_index=sqlc.arg(item_index)
AND (sqlc.arg(lease_fence)::text='' OR EXISTS(SELECT 1 FROM mailbox_import_jobs j WHERE j.tenant_id=sqlc.arg(tenant_id) AND j.mailbox_id=sqlc.arg(mailbox_id) AND j.id=sqlc.arg(job_id) AND j.lease_token=sqlc.arg(lease_fence)));

-- name: MailboxImportReservedBytes :one
SELECT COALESCE(sum(i.raw_bytes),0)::bigint FROM mailbox_import_items i
JOIN mailbox_import_jobs j ON j.tenant_id=i.tenant_id AND j.mailbox_id=i.mailbox_id AND j.id=i.job_id
WHERE i.tenant_id=$1 AND i.mailbox_id=$2 AND j.id<>$3 AND j.status NOT IN('completed','canceled')
AND i.status IN('pending','importing','unknown');

-- name: MailboxImportRecoverExpiredLeases :exec
UPDATE mailbox_import_items i SET status='unknown',error_code='MAIL_IMPORT_ACK_UNKNOWN',reconciled_at=NULL,reconcile_absent=false,updated_at=now()
FROM mailbox_import_jobs j WHERE i.tenant_id=$1 AND i.mailbox_id=$2 AND j.tenant_id=i.tenant_id AND j.mailbox_id=i.mailbox_id AND j.id=i.job_id
AND i.status='importing' AND (j.lease_until IS NULL OR j.lease_until<=now());

-- name: RecordMailboxImportAudit :exec
INSERT INTO audit_logs(tenant_id,action,target_id,detail)
SELECT j.tenant_id,sqlc.arg(action)::text,j.id::text,jsonb_build_object('mailbox_id',j.mailbox_id,'version',j.version,'detail',sqlc.arg(detail)::jsonb)
FROM mailbox_import_jobs j WHERE j.tenant_id=sqlc.arg(tenant_id) AND j.mailbox_id=sqlc.arg(mailbox_id) AND j.id=sqlc.arg(job_id);

-- name: MailboxImportItemViews :one
SELECT COALESCE(jsonb_agg(to_jsonb(i)-'raw_secret' ORDER BY item_index),'[]'::jsonb)::jsonb AS items
FROM mailbox_import_items i WHERE tenant_id=$1 AND mailbox_id=$2 AND job_id=$3;

-- name: LockMailboxImportTenantBudget :exec
SELECT pg_advisory_xact_lock(hashtextextended('urmail/mailimport/'||sqlc.arg(tenant_id)::bigint::text,0));
