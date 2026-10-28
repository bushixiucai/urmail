-- name: PlatformFindSchedulingEvent :one
SELECT * FROM mailbox_events WHERE tenant_id=$1 AND mailbox_id=$2 AND uid=$3 AND deleted_at IS NULL ORDER BY id LIMIT 1 FOR UPDATE;

-- name: PlatformBindSchedulingEvent :execrows
UPDATE mailbox_events SET organizer=$4,invitation_sequence=$5,invitation_stamp=$6,invitation_digest=$7,invitation_response=$8
WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND (organizer='' OR organizer=$4);

-- name: PlatformClaimCalendarResponse :exec
INSERT INTO mailbox_jobs(tenant_id,mailbox_id,kind,idempotency_key,payload,status)
VALUES($1,$2,'calendar_response',$3,$4,'uncertain');

-- name: PlatformFinishCalendarResponse :execrows
UPDATE mailbox_jobs SET status='done',updated_at=now() WHERE tenant_id=$1 AND mailbox_id=$2 AND kind='calendar_response' AND idempotency_key=$3 AND status='uncertain';

-- name: PlatformTakeReminderScan :many
WITH candidate AS (
 SELECT id FROM mailbox_events WHERE deleted_at IS NULL AND status<>'cancelled' AND reminders<>'[]'::jsonb
 ORDER BY reminder_scanned_at,id FOR UPDATE SKIP LOCKED LIMIT 50
)
UPDATE mailbox_events e SET reminder_scanned_at=now() FROM candidate c WHERE e.id=c.id RETURNING e.*;

-- name: PlatformScheduleCalendarReminder :exec
INSERT INTO mailbox_jobs(tenant_id,mailbox_id,kind,idempotency_key,payload,status,lease_until)
VALUES($1,$2,'calendar_reminder',$3,$4,'pending',$5)
ON CONFLICT(tenant_id,mailbox_id,kind,idempotency_key) DO UPDATE SET payload=EXCLUDED.payload,lease_until=EXCLUDED.lease_until,status='pending',last_error=NULL,updated_at=now() WHERE mailbox_jobs.status='pending' OR (mailbox_jobs.status='failed' AND mailbox_jobs.last_error='EVENT_CHANGED');

-- name: PlatformDiscardStaleCalendarReminders :exec
UPDATE mailbox_jobs j SET status='failed',last_error='EVENT_CHANGED',updated_at=now()
WHERE j.kind='calendar_reminder' AND j.status='pending' AND NOT EXISTS(
 SELECT 1 FROM mailbox_events e WHERE e.tenant_id=j.tenant_id AND e.mailbox_id=j.mailbox_id AND e.id=(j.payload->>'event_id')::bigint
 AND e.version=(j.payload->>'version')::bigint AND e.deleted_at IS NULL AND e.status<>'cancelled' AND e.reminders<>'[]'::jsonb);

-- name: PlatformPublishDueCalendarReminders :execrows
WITH candidate AS (
 SELECT j.id FROM mailbox_jobs j JOIN mailbox_events e ON e.tenant_id=j.tenant_id AND e.mailbox_id=j.mailbox_id AND e.id=(j.payload->>'event_id')::bigint
 WHERE j.kind='calendar_reminder' AND j.status='pending' AND j.lease_until<=now() AND e.version=(j.payload->>'version')::bigint
 AND e.deleted_at IS NULL AND e.status<>'cancelled'
 ORDER BY j.lease_until,j.id FOR UPDATE OF j SKIP LOCKED LIMIT 100
)
UPDATE mailbox_jobs j SET status='done',updated_at=now() FROM candidate c WHERE j.id=c.id;

-- name: PlatformListCalendarReminders :many
SELECT id,payload,created_at FROM mailbox_jobs WHERE tenant_id=$1 AND mailbox_id=$2 AND kind='calendar_reminder' AND status='done' AND COALESCE((payload->>'acknowledged')::boolean,false)=false
 AND (created_at>COALESCE((SELECT COALESCE((value->>'restored_at')::timestamptz,updated_at) FROM settings WHERE scope='platform' AND scope_id=0 AND key='restore_replay_review'),'-infinity'::timestamptz))
 ORDER BY id DESC LIMIT 50;

-- name: PlatformAcknowledgeCalendarReminder :execrows
UPDATE mailbox_jobs SET payload=jsonb_set(payload,'{acknowledged}','true'::jsonb),updated_at=now() WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND kind='calendar_reminder' AND status='done';

-- name: PlatformBindOwnedCalendarOrganizer :execrows
UPDATE mailbox_events e SET organizer=m.address,invitation_sequence=e.sequence
FROM mailboxes m WHERE e.tenant_id=$1 AND e.mailbox_id=$2 AND e.id=$3 AND m.tenant_id=e.tenant_id AND m.id=e.mailbox_id AND (e.organizer='' OR e.organizer=m.address);

-- name: PlatformRecordSchedulingReply :one
WITH next AS (
 UPDATE mailbox_events e SET attendees=$4,version=e.version+1,updated_at=now()
 WHERE e.tenant_id=$1 AND e.mailbox_id=$2 AND e.id=$3 AND e.version=$5 AND e.deleted_at IS NULL RETURNING *
), logged AS (
 INSERT INTO mailbox_changes(tenant_id,mailbox_id,resource_type,resource_id,version,deleted)
 SELECT tenant_id,mailbox_id,'event',id,version,false FROM next
)
SELECT * FROM next;

-- name: PlatformCalendarRestoreCutoff :one
SELECT COALESCE((value->>'restored_at')::timestamptz,updated_at)::timestamptz AS restored_at FROM settings WHERE scope='platform' AND scope_id=0 AND key='restore_replay_review';
