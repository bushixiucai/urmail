-- name: CreateOnboardingTask :one
INSERT INTO onboarding_tasks (tenant_id, domain_id, order_id, status, step, max_attempts) VALUES ($1,$2,$3,'pending',$4,$5) RETURNING *;

-- name: CreateOrderOnboardingTask :one
INSERT INTO onboarding_tasks (tenant_id, order_id, status, step, max_attempts,domain_id) VALUES ($1,$2,'pending',$3,$4,sqlc.narg(domain_id)) ON CONFLICT (tenant_id, order_id) WHERE order_id IS NOT NULL DO UPDATE SET order_id=EXCLUDED.order_id RETURNING *;

-- name: GetOnboardingTaskByDomain :one
SELECT * FROM onboarding_tasks WHERE tenant_id=$1 AND domain_id=$2 ORDER BY created_at DESC LIMIT 1;

-- name: ListOnboardingTasksByTenant :many
SELECT * FROM onboarding_tasks WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 50;

-- name: RecordDeliveryEvent :execrows
-- A conflict returns zero rows so the collector can recognize a safe replay.
-- Compatibility writes hash the typed canonical payload; unknown tenant IDs
-- remain NULL. The managed collector uses its physical source-event identity.
-- occurred_at must come from the source line, never the replay wall clock.
INSERT INTO delivery_events (tenant_id,instance,queue_id,message_id,sender,sender_domain,recipient,recipient_domain,relay,status,dsn,delay_seconds,response_detail,occurred_at,source_event_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,
 'compat:' || md5(jsonb_build_array($1::bigint,$2::text,$3::text,$4::text,$5::text,$6::text,$7::text,$8::text,$9::text,$10::text,$11::text,$12::double precision,$13::text,$14::timestamptz)::text))
ON CONFLICT DO NOTHING
RETURNING id;

-- name: ListDeliveryEventsByTenant :many
SELECT * FROM delivery_events WHERE tenant_id=$1 ORDER BY occurred_at DESC LIMIT 100;

-- name: CountDeliveryEventsByStatus :many
SELECT status, count(*) AS count FROM delivery_events WHERE tenant_id=$1 AND occurred_at >= $2 GROUP BY status;
