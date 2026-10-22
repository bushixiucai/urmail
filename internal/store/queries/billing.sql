-- name: GetPlanByCode :one
SELECT code, name, description, price_cents, currency, quota_bytes,
       mailbox_limit, domain_limit, daily_send_limit, features, sort_order, active
FROM plans
WHERE code = $1;

-- name: ListActivePlans :many
SELECT code, name, description, price_cents, currency, quota_bytes,
       mailbox_limit, domain_limit, daily_send_limit, features, sort_order, active
FROM plans
WHERE active = true
ORDER BY sort_order ASC, price_cents ASC;

-- name: GetActiveSubscriptionByTenant :one
SELECT id, tenant_id, plan_id, status, period_start, period_end, cancel_at_period_end, trial_ends_at, created_at
FROM subscriptions
WHERE tenant_id = $1 AND status IN ('active', 'trialing');

-- name: CancelActiveSubscriptionsForTenant :exec
-- Step 1 in atomic upgrade/downgrade replacement. past_due subscriptions must
-- be canceled too, otherwise the unique partial index
-- subscriptions_one_active_per_tenant (which covers past_due) makes the
-- follow-up INSERT fail (M3 review P2).
UPDATE subscriptions
SET status = 'canceled'
WHERE tenant_id = $1 AND status IN ('active', 'trialing', 'past_due');

-- name: CreateSubscription :one
-- Step 2 in atomic upgrade/downgrade replacement
INSERT INTO subscriptions (
    tenant_id, plan_id, status, period_start, period_end, cancel_at_period_end, trial_ends_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
) RETURNING id, tenant_id, plan_id, status, period_start, period_end, cancel_at_period_end, trial_ends_at, created_at;

-- name: CreateOrder :one
INSERT INTO orders (
    tenant_id, order_no, kind, status, amount_cents, currency, plan_code, channel_id, idempotency_key
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
) RETURNING id, tenant_id, order_no, kind, status, amount_cents, currency, plan_code, channel_id, idempotency_key, created_at, paid_at;

-- name: GetOrderByID :one
SELECT id, tenant_id, order_no, kind, status, amount_cents, currency, plan_code, channel_id, idempotency_key, created_at, paid_at
FROM orders
WHERE tenant_id = $1 AND id = $2;

-- name: UpdateOrderStatus :one
UPDATE orders
SET status = $3, paid_at = $4
WHERE tenant_id = $1 AND id = $2
RETURNING id, tenant_id, order_no, kind, status, amount_cents, currency, plan_code, channel_id, idempotency_key, created_at, paid_at;

-- name: CreatePayment :one
INSERT INTO payments (
    tenant_id, order_id, provider, provider_txn_id, amount_cents, currency, status, raw
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
) RETURNING id, tenant_id, order_id, provider, provider_txn_id, amount_cents, currency, status, raw, created_at, paid_at;

-- name: InsertPaymentEventPlaceholder :one
INSERT INTO payment_events (
    provider, provider_event_id, payload
) VALUES (
    $1, $2, $3
) ON CONFLICT (provider, provider_event_id) DO NOTHING
RETURNING id, provider, provider_event_id, processed_at;
