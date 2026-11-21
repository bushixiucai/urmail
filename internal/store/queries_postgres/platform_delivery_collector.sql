-- name: PlatformEnsureCollectorCheckpoint :exec
INSERT INTO collector_checkpoints(stream) VALUES($1) ON CONFLICT(stream) DO NOTHING;

-- name: PlatformGetCollectorCheckpoint :one
SELECT * FROM collector_checkpoints WHERE stream=$1;

-- name: PlatformLockCollectorCheckpoint :one
SELECT * FROM collector_checkpoints WHERE stream=$1 FOR UPDATE;

-- name: PlatformAdvanceCollectorCheckpoint :exec
UPDATE collector_checkpoints SET source_identity=$2,generation=$3,byte_offset=$4,
 gap_reason=COALESCE($5,gap_reason),observed_at=$6,updated_at=now() WHERE stream=$1;

-- name: PlatformGetDeliveryQueueContext :one
SELECT * FROM delivery_queue_contexts WHERE stream=$1 AND instance=$2 AND queue_id=$3;

-- name: PlatformPutDeliveryQueueContext :exec
INSERT INTO delivery_queue_contexts(stream,instance,queue_id,lifecycle_id,message_id,authenticated_mailbox,sender,started_at,closed)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT(stream,instance,queue_id) DO UPDATE SET lifecycle_id=EXCLUDED.lifecycle_id,
 message_id=EXCLUDED.message_id,authenticated_mailbox=EXCLUDED.authenticated_mailbox,
 sender=EXCLUDED.sender,started_at=EXCLUDED.started_at,closed=EXCLUDED.closed,updated_at=now();

-- name: PlatformResetDeliveryQueueContexts :exec
DELETE FROM delivery_queue_contexts WHERE stream=$1;

-- name: PlatformResolveDeliveryMailboxTenant :one
SELECT m.tenant_id FROM mailboxes m JOIN domains d ON d.id=m.domain_id AND d.tenant_id=m.tenant_id
WHERE m.address=$1 AND d.verified_at IS NOT NULL AND d.verified_at<=statement_timestamp();

-- name: PlatformRecordCollectedDeliveryEvent :execrows
INSERT INTO delivery_events(tenant_id,instance,queue_id,message_id,sender,sender_domain,
 recipient,recipient_domain,relay,status,dsn,delay_seconds,response_detail,occurred_at,source_event_id,lifecycle_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
ON CONFLICT DO NOTHING;

-- name: PlatformCollectorHealth :many
SELECT stream,source_identity,generation,byte_offset,gap_reason,observed_at,updated_at
FROM collector_checkpoints ORDER BY stream;

-- name: PlatformQueueAuthenticatedTenant :one
SELECT m.tenant_id FROM delivery_queue_contexts c
JOIN mailboxes m ON m.address=c.authenticated_mailbox
WHERE c.stream=$1 AND c.instance=$2 AND c.queue_id=$3 AND c.closed=false
  AND c.started_at >= $4::timestamptz - interval '5 seconds'
  AND c.started_at <= $4::timestamptz + interval '5 seconds';

-- name: PlatformAttributeAuthenticatedDeliveryLifecycle :exec
UPDATE delivery_events SET tenant_id=$3
WHERE instance=$1 AND lifecycle_id=$2 AND tenant_id IS NULL;

-- name: PlatformAcknowledgeCollectorGap :execrows
UPDATE collector_checkpoints SET gap_reason=NULL,updated_at=statement_timestamp()
WHERE stream= @stream AND generation= @generation AND gap_reason IS NOT NULL
 AND observed_at>statement_timestamp()-interval '2 minutes' AND observed_at<=statement_timestamp()+interval '1 minute';
