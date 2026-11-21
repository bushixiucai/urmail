-- name: PlatformListContactsPage :many
SELECT * FROM mailbox_contacts WHERE tenant_id=$1 AND mailbox_id=$2 AND deleted_at IS NULL AND id>$3 ORDER BY id LIMIT $4;

-- name: PlatformGetContactGroup :one
SELECT * FROM mailbox_contact_groups WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND deleted_at IS NULL;

-- name: PlatformListContactGroupMembers :many
SELECT c.* FROM mailbox_contacts c JOIN mailbox_contact_group_members m ON m.tenant_id=c.tenant_id AND m.mailbox_id=c.mailbox_id AND m.contact_id=c.id
JOIN mailbox_contact_groups g ON g.tenant_id=m.tenant_id AND g.mailbox_id=m.mailbox_id AND g.id=m.group_id
WHERE c.tenant_id=$1 AND c.mailbox_id=$2 AND g.id=$3 AND c.deleted_at IS NULL AND g.deleted_at IS NULL ORDER BY c.id;

-- name: PlatformRemoveContactGroupMember :execrows
DELETE FROM mailbox_contact_group_members m USING mailbox_contact_groups g
WHERE m.tenant_id=$1 AND m.mailbox_id=$2 AND m.group_id=$3 AND m.contact_id=$4
AND g.tenant_id=m.tenant_id AND g.mailbox_id=m.mailbox_id AND g.id=m.group_id AND g.deleted_at IS NULL;

-- name: PlatformContactImportDuplicate :one
SELECT EXISTS(SELECT 1 FROM mailbox_contacts WHERE tenant_id=$1 AND mailbox_id=$2 AND deleted_at IS NULL
AND display_name=$3 AND given_name=$4 AND family_name=$5 AND COALESCE(NULLIF(emails,'null'::jsonb),'[]'::jsonb)=COALESCE(NULLIF($6::jsonb,'null'::jsonb),'[]'::jsonb) AND COALESCE(NULLIF(phones,'null'::jsonb),'[]'::jsonb)=COALESCE(NULLIF($7::jsonb,'null'::jsonb),'[]'::jsonb) AND note=$8);

-- name: PlatformGetDataImportReceipt :one
SELECT * FROM mailbox_jobs WHERE tenant_id=$1 AND mailbox_id=$2 AND kind=$3 AND idempotency_key=$4 FOR UPDATE;

-- name: PlatformInsertDataImportReceipt :exec
INSERT INTO mailbox_jobs(tenant_id,mailbox_id,kind,idempotency_key,payload,status) VALUES($1,$2,$3,$4,$5,'done');

-- name: PlatformLockDataImport :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(tenant_id)::text || ':' || sqlc.arg(mailbox_id)::text || ':' || sqlc.arg(idempotency_key)::text, 782314));

-- name: PlatformListDataImportReceipts :many
SELECT id,kind,idempotency_key,payload,created_at FROM mailbox_jobs
WHERE tenant_id=$1 AND mailbox_id=$2 AND kind IN('import_contacts','import_calendar') AND status='done'
ORDER BY id DESC LIMIT 20;

-- name: PlatformListCalendarEventsPage :many
SELECT * FROM mailbox_events
WHERE tenant_id=$1 AND mailbox_id=$2 AND deleted_at IS NULL AND id>$3
AND (rrule<>'' OR (starts_at<sqlc.arg(range_end) AND ends_at>sqlc.arg(range_start)))
ORDER BY id LIMIT sqlc.arg(page_limit);

-- name: PlatformLockCalendarInvitationEvent :one
SELECT * FROM mailbox_events WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND deleted_at IS NULL FOR UPDATE;

-- name: PlatformClaimCalendarInvitation :exec
INSERT INTO mailbox_jobs(tenant_id,mailbox_id,kind,idempotency_key,payload,status)
VALUES($1,$2,'calendar_invitation',$3,$4,'uncertain');

-- name: PlatformFinishCalendarInvitation :execrows
UPDATE mailbox_jobs SET status='done',updated_at=now()
WHERE tenant_id=$1 AND mailbox_id=$2 AND kind='calendar_invitation' AND idempotency_key=$3 AND status='uncertain';

-- name: PlatformClaimMailTransfer :exec
INSERT INTO mailbox_jobs(tenant_id,mailbox_id,kind,idempotency_key,payload,status) VALUES($1,$2,'mail_transfer',$3,$4,'uncertain');

-- name: PlatformCompleteMailTransfer :execrows
UPDATE mailbox_jobs SET payload=$4,status=$5,updated_at=now()
WHERE tenant_id=$1 AND mailbox_id=$2 AND kind='mail_transfer' AND idempotency_key=$3 AND status='uncertain';
