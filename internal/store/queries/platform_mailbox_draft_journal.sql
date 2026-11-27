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

-- PlatformAcquireDraftSessionLock 在 S5 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- PG 的会话级 advisory lock 跨多个事务持有，丢弃连接才释放；SQLite 无会话锁，
-- 改为进程内按 (tenant,mailbox,folder,validity,uid) 键控的互斥量，须由
-- PlatformReleaseDraftSessionLock 配对释放，详见 scripts/sqlite/lock-semantics.md。


-- PlatformReleaseDraftSessionLock 在 S5 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- 释放 PlatformAcquireDraftSessionLock 持有的进程内互斥量；未持有时返回 false。


-- name: PlatformSetDraftSaveScope :exec
-- scope: platform

SELECT set_config('urmail.tenant_id',sqlc.arg(tenant_id),true),set_config('urmail.mailbox_id',sqlc.arg(mailbox_id),true);
