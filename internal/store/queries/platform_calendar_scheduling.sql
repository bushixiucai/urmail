-- name: PlatformFindSchedulingEvent :one
-- scope: platform

SELECT * FROM mailbox_events WHERE tenant_id=?1 AND mailbox_id=?2 AND uid=?3 AND deleted_at IS NULL ORDER BY id LIMIT 1;

-- name: PlatformBindSchedulingEvent :execrows
-- scope: platform

UPDATE mailbox_events SET organizer=?4,invitation_sequence=?5,invitation_stamp=?6,invitation_digest=?7,invitation_response=?8
WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND (organizer='' OR organizer=?4);

-- name: PlatformClaimCalendarResponse :exec
-- scope: platform

INSERT INTO mailbox_jobs(tenant_id,mailbox_id,kind,idempotency_key,payload,status)
VALUES(?1,?2,'calendar_response',?3,?4,'uncertain');

-- name: PlatformFinishCalendarResponse :execrows
-- scope: platform

UPDATE mailbox_jobs SET status='done',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE tenant_id=?1 AND mailbox_id=?2 AND kind='calendar_response' AND idempotency_key=?3 AND status='uncertain';

-- name: PlatformTakeReminderScan :many
-- scope: platform

WITH candidate AS (
 SELECT id FROM mailbox_events WHERE deleted_at IS NULL AND status<>'cancelled' AND reminders<>'[]'
 ORDER BY reminder_scanned_at,id LIMIT 50
)
UPDATE mailbox_events SET reminder_scanned_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE id=(SELECT id FROM candidate) AND reminder_scanned_at IS NULL
RETURNING *;

-- name: PlatformScheduleCalendarReminder :exec
-- scope: platform

INSERT INTO mailbox_jobs(tenant_id,mailbox_id,kind,idempotency_key,payload,status,lease_until)
VALUES(?1,?2,'calendar_reminder',?3,?4,'pending',?5)
ON CONFLICT(tenant_id,mailbox_id,kind,idempotency_key) DO UPDATE SET payload=EXCLUDED.payload,lease_until=EXCLUDED.lease_until,status='pending',last_error=NULL,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE mailbox_jobs.status='pending' OR (mailbox_jobs.status='failed' AND mailbox_jobs.last_error='EVENT_CHANGED');

-- name: PlatformDiscardStaleCalendarReminders :exec
-- scope: platform

UPDATE mailbox_jobs SET status='failed',last_error='EVENT_CHANGED',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE kind='calendar_reminder' AND status='pending' AND NOT EXISTS(
 SELECT 1  WHERE EXISTS(SELECT 1 FROM mailbox_events e WHERE e.tenant_id=tenant_id AND e.mailbox_id=mailbox_id AND e.id=(json_extract(payload, '$.event_id')) AND e.version=(json_extract(payload, '$.version')) AND e.deleted_at IS NULL AND e.status<>'cancelled' AND e.reminders<>'[]'));-- name: PlatformPublishDueCalendarReminders :execrows
-- scope: platform

WITH candidate AS (
 SELECT id FROM mailbox_jobs j JOIN mailbox_events e ON e.tenant_id=tenant_id AND e.mailbox_id=mailbox_id AND e.id=(json_extract(payload, '$.event_id'))
 WHERE kind='calendar_reminder' AND status='pending' AND lease_until<=(CAST(strftime('%s','now') AS INTEGER)*1000000) AND e.version=(json_extract(payload, '$.version'))
 AND e.deleted_at IS NULL AND e.status<>'cancelled'
 ORDER BY lease_until,id LIMIT 100
)
UPDATE mailbox_jobs SET status='done',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE id=(SELECT id FROM candidate) AND status='pending';

-- name: PlatformListCalendarReminders :many
-- scope: platform

SELECT id,payload,created_at FROM mailbox_jobs WHERE tenant_id=?1 AND mailbox_id=?2 AND kind='calendar_reminder' AND status='done' AND COALESCE((json_extract(payload, '$.acknowledged')),false)=false
 AND (created_at>COALESCE((SELECT COALESCE((json_extract(value, '$.restored_at')),updated_at) FROM settings WHERE scope='platform' AND scope_id=0 AND key='restore_replay_review'),'-infinity'))
 ORDER BY id DESC LIMIT 50;

-- name: PlatformAcknowledgeCalendarReminder :execrows
-- scope: platform

UPDATE mailbox_jobs SET payload=jsonb_set(payload,'{acknowledged}','true'),updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND kind='calendar_reminder' AND status='done';

-- name: PlatformBindOwnedCalendarOrganizer :execrows
-- scope: platform

UPDATE mailbox_events SET organizer=m.address,invitation_sequence=sequence
 WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND EXISTS(SELECT 1 FROM mailboxes m WHERE m.tenant_id=tenant_id AND m.id=mailbox_id AND (organizer='' OR organizer=m.address));-- name: PlatformRecordSchedulingReply :one
-- scope: platform

UPDATE mailbox_events SET attendees=?4,version=version+1,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND version=?5 AND deleted_at IS NULL RETURNING *;

-- name: PlatformCalendarRestoreCutoff :one
-- scope: platform

SELECT COALESCE((json_extract(value, '$.restored_at')),updated_at) AS restored_at FROM settings WHERE scope='platform' AND scope_id=0 AND key='restore_replay_review';
