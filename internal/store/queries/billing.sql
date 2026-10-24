-- name: GetPlanByCode :one
SELECT code,name,description,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features,sort_order,active FROM plans WHERE code=$1;

-- name: ListActivePlans :many
SELECT code,name,description,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features,sort_order,active FROM plans WHERE active=true ORDER BY sort_order,price_cents;

-- name: GetActiveSubscriptionByTenant :one
SELECT id,tenant_id,plan_id,status,period_start,period_end,cancel_at_period_end,trial_ends_at,created_at,billing_cycle,plan_snapshot,entitlement_source,migration_manifest_sha256 FROM subscriptions WHERE tenant_id=$1 AND status IN('active','trialing','past_due') ORDER BY created_at DESC,id DESC LIMIT 1;

-- name: CreateSubscription :one
INSERT INTO subscriptions(tenant_id,plan_id,status,period_start,period_end,cancel_at_period_end,trial_ends_at) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING *;

-- name: CreateOrder :one
INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code,channel_id,idempotency_key,billing_cycle) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id,tenant_id,order_no,kind,status,amount_cents,currency,plan_code,channel_id,idempotency_key,created_at,paid_at,billing_cycle,quote_digest;

-- name: GetOrderByID :one
SELECT id,tenant_id,order_no,kind,status,amount_cents,currency,plan_code,channel_id,idempotency_key,created_at,paid_at,billing_cycle,quote_digest FROM orders WHERE tenant_id=$1 AND id=$2;

-- name: UpdateOrderStatus :one
UPDATE orders SET status=$3,paid_at=$4 WHERE tenant_id=$1 AND id=$2 RETURNING id,tenant_id,order_no,kind,status,amount_cents,currency,plan_code,channel_id,idempotency_key,created_at,paid_at,billing_cycle,quote_digest;

-- name: CreatePayment :one
INSERT INTO payments(tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status,raw,paid_at,account_scope) VALUES($1,$2,$3,$4,$5,$6,$7,$8,now(),COALESCE(NULLIF(sqlc.arg(account_scope)::text,''),'legacy')) ON CONFLICT(provider,account_scope,provider_txn_id) DO NOTHING RETURNING id,tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status,raw,created_at,paid_at,account_scope;





-- name: GetPaymentForTenant :one
SELECT id,tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status,raw,created_at,paid_at,account_scope FROM payments WHERE tenant_id=$1 AND id=$2;

-- name: ListPaymentsByTenant :many
SELECT id,tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status,raw,created_at,paid_at,account_scope FROM payments WHERE tenant_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100;

-- name: ListOrdersByTenant :many
SELECT id,tenant_id,order_no,kind,status,amount_cents,currency,plan_code,channel_id,idempotency_key,created_at,paid_at,billing_cycle,quote_digest FROM orders WHERE tenant_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100;

-- name: GetCheckoutOrderByIdempotencyKey :one
SELECT id,tenant_id,order_no,kind,status,amount_cents,currency,plan_code,channel_id,idempotency_key,created_at,paid_at,billing_cycle,quote_digest FROM orders WHERE tenant_id=$1 AND idempotency_key=$2;

-- name: CreateRefund :one
-- Atomic routine owns locking, validation, capacity check and insert.
SELECT * FROM urmail_create_refund(sqlc.arg(tenant_id)::bigint,sqlc.arg(order_id)::bigint,sqlc.arg(payment_id)::bigint,sqlc.arg(refund_no)::text,sqlc.arg(amount_cents)::bigint,sqlc.arg(currency)::text,sqlc.arg(reason)::text);

-- name: ListRefundsByTenant :many
SELECT id,tenant_id,order_id,payment_id,refund_no,amount_cents,currency,reason,status,provider_refund_no,created_at,completed_at,requested_amount_cents,approved_by,approved_at,approval_reason,entitlement_action,entitlement_effective_at,target_term_order_id,entitlement_applied_at,last_error_code,request_started_at FROM refunds WHERE tenant_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100;

-- Releasing a reservation removes the redemption row entirely, so the coupon
-- capacity trigger hands the quota back and the tenant can redeem again. The
-- refund guard trigger refuses to release a settled order's redemption.
-- name: ReleaseCouponRedemption :execrows
DELETE FROM coupon_redemptions WHERE coupon_code=$1 AND tenant_id=$2 AND order_id=$3;

-- A pending order can be withdrawn by its tenant while nobody has paid. The
-- coupon reservation (if any) is released by the caller in the same
-- transaction, so the quota is returned atomically with the cancellation.
-- name: CancelPendingOrder :one
UPDATE orders SET status='canceled'
WHERE tenant_id=$1 AND id=$2 AND status='pending'
RETURNING id,tenant_id,order_no,kind,status,amount_cents,currency,plan_code,channel_id,idempotency_key,created_at,paid_at,billing_cycle,quote_digest;

-- name: GetOrderForTenant :one
SELECT id,tenant_id,order_no,kind,status,amount_cents,currency,plan_code,channel_id,idempotency_key,created_at,paid_at,billing_cycle,quote_digest
FROM orders WHERE tenant_id=$1 AND id=$2;
