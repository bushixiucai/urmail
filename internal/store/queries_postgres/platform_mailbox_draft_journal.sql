-- name: PlatformClaimDraftSave :exec
INSERT INTO mailbox_jobs(tenant_id, mailbox_id, kind, idempotency_key, payload, status)
VALUES($1,$2,'draft_save',$3,$4,'uncertain');

-- name: PlatformCompleteDraftSave :execrows
UPDATE mailbox_jobs SET payload=$4,status='done',updated_at=now()
WHERE tenant_id=$1 AND mailbox_id=$2 AND kind='draft_save' AND idempotency_key=$3 AND status IN('uncertain','done');

-- name: PlatformDraftReplacementPending :one
SELECT EXISTS(SELECT 1 FROM mailbox_jobs WHERE tenant_id=$1 AND mailbox_id=$2 AND kind='draft_save'
AND status='uncertain' AND payload->>'previous_uid'=sqlc.arg(previous_uid)::text
AND payload->>'previous_validity'=sqlc.arg(previous_validity)::text);

-- name: PlatformAcquireDraftSessionLock :exec
SELECT pg_advisory_lock(hashtextextended(sqlc.arg(tenant_id)::text||':'||sqlc.arg(mailbox_id)::text||':'||sqlc.arg(folder)::text||':'||sqlc.arg(validity)::text||':'||sqlc.arg(uid)::text, 974152));

-- name: PlatformReleaseDraftSessionLock :one
SELECT pg_advisory_unlock(hashtextextended(sqlc.arg(tenant_id)::text||':'||sqlc.arg(mailbox_id)::text||':'||sqlc.arg(folder)::text||':'||sqlc.arg(validity)::text||':'||sqlc.arg(uid)::text, 974152));

-- name: PlatformSetDraftSaveScope :exec
SELECT set_config('urmail.tenant_id',sqlc.arg(tenant_id)::text,true),set_config('urmail.mailbox_id',sqlc.arg(mailbox_id)::text,true);
