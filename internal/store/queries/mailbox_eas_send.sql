-- name: ClaimEASSend :one
INSERT INTO mailbox_jobs (tenant_id,mailbox_id,kind,idempotency_key,payload,status,attempts)
VALUES ($1,$2,'eas_sendmail',$3,$4,'uncertain',1)
ON CONFLICT (tenant_id,mailbox_id,kind,idempotency_key) DO NOTHING RETURNING id;

-- name: GetEASSend :one
SELECT id,payload,status FROM mailbox_jobs WHERE tenant_id=$1 AND mailbox_id=$2 AND kind='eas_sendmail' AND idempotency_key=$3 FOR UPDATE;

-- name: FinishEASSend :exec
UPDATE mailbox_jobs SET status=$4,payload=$5,last_error=$6,updated_at=now()
WHERE tenant_id=$1 AND mailbox_id=$2 AND kind='eas_sendmail' AND id=$3;

-- name: LockEASSendCopy :one
SELECT payload,status FROM mailbox_jobs WHERE tenant_id=$1 AND mailbox_id=$2 AND kind='eas_sendmail' AND id=$3 FOR UPDATE;
