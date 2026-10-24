-- name: CreateOnboardingTask :one
INSERT INTO onboarding_tasks (tenant_id, domain_id, order_id, status, step, max_attempts) VALUES ($1,$2,$3,'pending',$4,$5) RETURNING *;

-- name: CreateOrderOnboardingTask :one
INSERT INTO onboarding_tasks (tenant_id, order_id, status, step, max_attempts) VALUES ($1,$2,'pending',$3,$4) ON CONFLICT (tenant_id, order_id) WHERE order_id IS NOT NULL DO UPDATE SET order_id=EXCLUDED.order_id RETURNING *;

-- name: GetOnboardingTaskByDomain :one
SELECT * FROM onboarding_tasks WHERE tenant_id=$1 AND domain_id=$2 ORDER BY created_at DESC LIMIT 1;

-- name: ListOnboardingTasksByTenant :many
SELECT * FROM onboarding_tasks WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 50;

-- name: RecordDeliveryEvent :execrows
-- A conflict returns zero rows so the collector can recognize a safe replay.
-- The conflict target mirrors delivery_events_dedupe_key exactly: instance is
-- part of the identity (queue IDs are per Postfix instance) and status is part
-- of it too, so a deferred -> sent transition is recorded instead of being
-- silently swallowed by the earlier row. occurred_at MUST come from the log
-- line, never from the wall clock, or replays double-count.
INSERT INTO delivery_events (tenant_id,instance,queue_id,message_id,sender,sender_domain,recipient,recipient_domain,relay,status,dsn,delay_seconds,response_detail,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (tenant_id, instance, queue_id, COALESCE(recipient, ''), occurred_at, status) DO NOTHING
RETURNING id;

-- name: ListDeliveryEventsByTenant :many
SELECT * FROM delivery_events WHERE tenant_id=$1 ORDER BY occurred_at DESC LIMIT 100;

-- name: CountDeliveryEventsByStatus :many
SELECT status, count(*) AS count FROM delivery_events WHERE tenant_id=$1 AND occurred_at >= $2 GROUP BY status;
