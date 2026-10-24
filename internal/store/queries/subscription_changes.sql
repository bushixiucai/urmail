-- name: ScheduleSubscriptionChange :one
INSERT INTO subscription_changes(tenant_id,target_plan_code,effective_at,requested_by)
VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id) DO UPDATE SET target_plan_code=EXCLUDED.target_plan_code,effective_at=EXCLUDED.effective_at,status='scheduled',requested_by=EXCLUDED.requested_by,created_at=now() RETURNING *;

-- name: GetSubscriptionChange :one
SELECT * FROM subscription_changes WHERE tenant_id=$1 AND status='scheduled';

-- name: CancelSubscriptionAtPaidEnd :one
UPDATE subscriptions SET cancel_at_period_end=$2 WHERE tenant_id=$1 AND status IN('active','trialing','past_due') RETURNING *;

-- name: GetSubscriptionChangeStatus :one
SELECT * FROM subscription_changes WHERE tenant_id=$1;

-- name: CancelScheduledSubscriptionChange :one
UPDATE subscription_changes SET status='canceled',requested_by=$2 WHERE tenant_id=$1 AND status IN('scheduled','blocked') RETURNING *;
