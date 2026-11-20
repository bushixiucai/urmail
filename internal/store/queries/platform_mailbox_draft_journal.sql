-- name: PlatformClaimDraftSave :exec
-- scope: platform

INSERT INTO mailbox_jobs(tenant_id, mailbox_id, kind, idempotency_key, payload, status)
VALUES(?1,?2,'draft_save',?3,?4,'uncertain');

-- name: PlatformCompleteDraftSave :execrows
-- scope: platform

UPDATE mailbox_jobs SET payload=?4,status='done',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND mailbox_id=?2 AND kind='draft_save' AND idempotency_key=?3 AND status IN('uncertain','done');

-- name: PlatformDraftReplacementPending :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM mailbox_jobs WHERE tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND kind='draft_save'
AND status='uncertain' AND json_extract(payload, '$.previous_uid')=sqlc.arg(previous_uid)
AND json_extract(payload, '$.previous_validity')=sqlc.arg(previous_validity));

-- name: PlatformAcquireDraftSessionLock :exec
-- scope: platform

SELECT pg_advisory_lock(hashtextextended(sqlc.arg(tenant_id)||':'||sqlc.arg(mailbox_id)||':'||sqlc.arg(folder)||':'||sqlc.arg(validity)||':'||sqlc.arg(uid), 974152));

-- name: PlatformReleaseDraftSessionLock :one
-- scope: platform

SELECT pg_advisory_unlock(hashtextextended(sqlc.arg(tenant_id)||':'||sqlc.arg(mailbox_id)||':'||sqlc.arg(folder)||':'||sqlc.arg(validity)||':'||sqlc.arg(uid), 974152));

-- name: PlatformSetDraftSaveScope :exec
-- scope: platform

SELECT set_config('urmail.tenant_id',sqlc.arg(tenant_id),true),set_config('urmail.mailbox_id',sqlc.arg(mailbox_id),true);
