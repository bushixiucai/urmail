-- name: PlatformListContactsPage :many
-- scope: platform

SELECT * FROM mailbox_contacts WHERE tenant_id=?1 AND mailbox_id=?2 AND deleted_at IS NULL AND id>?3 ORDER BY id LIMIT ?4;

-- name: PlatformGetContactGroup :one
-- scope: platform

SELECT * FROM mailbox_contact_groups WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND deleted_at IS NULL;

-- name: PlatformListContactGroupMembers :many
-- scope: platform

SELECT c.* FROM mailbox_contacts c JOIN mailbox_contact_group_members m ON m.tenant_id=c.tenant_id AND m.mailbox_id=c.mailbox_id AND m.contact_id=c.id
JOIN mailbox_contact_groups g ON g.tenant_id=m.tenant_id AND g.mailbox_id=m.mailbox_id AND g.id=m.group_id
WHERE c.tenant_id=?1 AND c.mailbox_id=?2 AND g.id=?3 AND c.deleted_at IS NULL AND g.deleted_at IS NULL ORDER BY c.id;

-- name: PlatformRemoveContactGroupMember :execrows
-- scope: platform

DELETE FROM mailbox_contact_group_members m USING mailbox_contact_groups g
WHERE m.tenant_id=?1 AND m.mailbox_id=?2 AND m.group_id=?3 AND m.contact_id=?4
AND g.tenant_id=m.tenant_id AND g.mailbox_id=m.mailbox_id AND g.id=m.group_id AND g.deleted_at IS NULL;

-- name: PlatformContactImportDuplicate :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM mailbox_contacts WHERE tenant_id=?1 AND mailbox_id=?2 AND deleted_at IS NULL
AND display_name=?3 AND given_name=?4 AND family_name=?5 AND COALESCE(NULLIF(emails,'null'),'[]')=COALESCE(NULLIF(?6,'null'),'[]') AND COALESCE(NULLIF(phones,'null'),'[]')=COALESCE(NULLIF(?7,'null'),'[]') AND note=?8);

-- name: PlatformGetDataImportReceipt :one
-- scope: platform

SELECT * FROM mailbox_jobs WHERE tenant_id=?1 AND mailbox_id=?2 AND kind=?3 AND idempotency_key=?4;

-- name: PlatformInsertDataImportReceipt :exec
-- scope: platform

INSERT INTO mailbox_jobs(tenant_id,mailbox_id,kind,idempotency_key,payload,status) VALUES(?1,?2,?3,?4,?5,'done');

-- PlatformLockDataImport 在 S5 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- PG 用事务级 advisory lock 串行化同一幂等键的数据导入；SQLite 改为单写者事务
-- 串行（BEGIN IMMEDIATE），详见 scripts/sqlite/lock-semantics.md。


-- name: PlatformListDataImportReceipts :many
-- scope: platform

SELECT id,kind,idempotency_key,payload,created_at FROM mailbox_jobs
WHERE tenant_id=?1 AND mailbox_id=?2 AND kind IN('import_contacts','import_calendar') AND status='done'
ORDER BY id DESC LIMIT 20;

-- name: PlatformListCalendarEventsPage :many
-- scope: platform

SELECT * FROM mailbox_events
WHERE tenant_id=?1 AND mailbox_id=?2 AND deleted_at IS NULL AND id>?3
AND (rrule<>'' OR (starts_at<sqlc.arg(range_end) AND ends_at>sqlc.arg(range_start)))
ORDER BY id LIMIT sqlc.arg(page_limit);

-- name: PlatformLockCalendarInvitationEvent :one
-- scope: platform

SELECT * FROM mailbox_events WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND deleted_at IS NULL;

-- name: PlatformClaimCalendarInvitation :exec
-- scope: platform

INSERT INTO mailbox_jobs(tenant_id,mailbox_id,kind,idempotency_key,payload,status)
VALUES(?1,?2,'calendar_invitation',?3,?4,'uncertain');

-- name: PlatformFinishCalendarInvitation :execrows
-- scope: platform

UPDATE mailbox_jobs SET status='done',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND mailbox_id=?2 AND kind='calendar_invitation' AND idempotency_key=?3 AND status='uncertain';

-- name: PlatformClaimMailTransfer :exec
-- scope: platform

INSERT INTO mailbox_jobs(tenant_id,mailbox_id,kind,idempotency_key,payload,status) VALUES(?1,?2,'mail_transfer',?3,?4,'uncertain');

-- name: PlatformCompleteMailTransfer :execrows
-- scope: platform

UPDATE mailbox_jobs SET payload=?4,status=?5,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND mailbox_id=?2 AND kind='mail_transfer' AND idempotency_key=?3 AND status='uncertain';
