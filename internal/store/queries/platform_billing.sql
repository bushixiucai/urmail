-- Platform-side billing settlement queries.
-- Settlement runs inside a platform transaction (it is driven by provider
-- webhooks and the manual settlement path, neither of which has a tenant
-- session), so these queries are exempt from the tenant_id predicate per
-- AGENTS.md and therefore live in a platform_*.sql file. AGENTS.md also
-- forbids hand-written SQL in handlers: order_settle.go must go through these
-- statements only (M5 review 3.16).

-- name: PlatformGetOrderTenantID :one
SELECT tenant_id FROM orders WHERE id = $1;

-- name: PlatformGetLatestActiveSubscriptionPeriodEnd :one
SELECT period_end FROM subscriptions
WHERE tenant_id = $1 AND status IN ('active', 'trialing', 'past_due')
ORDER BY period_end DESC LIMIT 1;

-- name: PlatformCancelActiveSubscriptions :execrows
UPDATE subscriptions SET status = 'canceled'
WHERE tenant_id = $1 AND status IN ('active', 'trialing', 'past_due');

-- name: PlatformCreateSubscriptionForOrder :one
INSERT INTO subscriptions (tenant_id, plan_id, status, period_start, period_end, billing_cycle)
VALUES ($1, $2, 'active', $3, $4, $5)
RETURNING id, tenant_id, plan_id, status, period_start, period_end, cancel_at_period_end, trial_ends_at, created_at, billing_cycle;

-- name: PlatformCreateSettlementInvoice :one
INSERT INTO invoices (tenant_id, order_id, number, period_start, period_end, amount_cents, currency, status, issued_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'paid', now())
RETURNING id, tenant_id, order_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at;

-- name: PlatformCancelPendingOrder :one
UPDATE orders SET status = 'canceled'
WHERE id = $1 AND status = 'pending'
RETURNING id, tenant_id, order_no, kind, status, amount_cents, currency, plan_code, channel_id, idempotency_key, created_at, paid_at, billing_cycle;

-- name: PlatformReleaseCouponRedemption :execrows
DELETE FROM coupon_redemptions WHERE coupon_code = $1 AND order_id = $2;
