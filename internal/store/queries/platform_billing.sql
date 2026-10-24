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
INSERT INTO subscriptions (tenant_id, plan_id, status, period_start, period_end, billing_cycle, plan_snapshot)
VALUES ($1, $2, 'active', $3, $4, $5, $6)
RETURNING id, tenant_id, plan_id, status, period_start, period_end, cancel_at_period_end, trial_ends_at, created_at, billing_cycle, plan_snapshot;

-- name: PlatformCreateSettlementInvoice :one
INSERT INTO invoices (tenant_id, order_id, number, period_start, period_end, amount_cents, currency, status, issued_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'paid', now())
RETURNING id, tenant_id, order_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at, document_snapshot, source_payment_id;

-- name: PlatformCancelPendingOrder :one
UPDATE orders SET status = 'canceled'
WHERE id = $1 AND status = 'pending'
RETURNING id, tenant_id, order_no, kind, status, amount_cents, currency, plan_code, channel_id, idempotency_key, created_at, paid_at, billing_cycle, quote_digest;

-- name: PlatformReleaseCouponRedemption :execrows
DELETE FROM coupon_redemptions WHERE coupon_code = $1 AND order_id = $2;

-- name: PlatformGetSubscriptionForSettlement :one
SELECT id,tenant_id,plan_id,status,period_start,period_end,billing_cycle,plan_snapshot
FROM subscriptions WHERE tenant_id=$1 AND status IN('active','trialing','past_due') FOR UPDATE;

-- name: PlatformGetOrderQuote :one
SELECT q.payload,q.plan_snapshot,q.document_snapshot FROM orders o JOIN billing_quotes q ON q.tenant_id=o.tenant_id AND q.digest=o.quote_digest WHERE o.id=$1;

-- name: PlatformFreezeSubscriptionSnapshot :exec
UPDATE subscriptions SET plan_snapshot=$2 WHERE id=$1;

-- name: PlatformLockBillingTenant :exec
SELECT pg_advisory_xact_lock($1::bigint);

-- name: PlatformSetPlanPrice :one
INSERT INTO plan_prices(plan_code,currency,billing_cycle,amount_cents) VALUES($1,$2,$3,$4)
ON CONFLICT(plan_code,currency,billing_cycle) DO UPDATE SET amount_cents=EXCLUDED.amount_cents,version=DEFAULT,updated_at=now()
RETURNING plan_code,currency,billing_cycle,amount_cents,version;

-- name: PlatformRecordSubscriptionTerm :exec
INSERT INTO subscription_terms(order_id,tenant_id,period_start,period_end,plan_snapshot,base_amount_cents,currency,billing_cycle,billing_period_start,basis_order_id,calendar_snapshot)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11);
-- name: PlatformBindInvoicePayment :exec
UPDATE invoices SET source_payment_id=$3 WHERE tenant_id=$1 AND order_id=$2 AND source_payment_id IS NULL;

-- name: PlatformLegacyPeriodRepairCandidate :one
SELECT s.id AS subscription_id,s.tenant_id,s.period_start,s.period_end,s.plan_snapshot,
o.id AS order_id,o.paid_at,o.currency,o.billing_cycle,p.id AS payment_id,o.amount_cents
FROM subscriptions s JOIN orders o ON o.tenant_id=s.tenant_id AND o.id=sqlc.arg(order_id)
JOIN payments p ON p.tenant_id=o.tenant_id AND p.order_id=o.id AND p.status='succeeded'
WHERE s.tenant_id=sqlc.arg(tenant_id) AND s.id=sqlc.arg(subscription_id) AND s.status='active'
AND s.plan_id<>'free' AND o.plan_code=s.plan_id AND o.status='paid' AND o.paid_at<=now() AND o.amount_cents>0
AND p.amount_cents=o.amount_cents AND p.currency=o.currency AND s.period_start>now()
AND s.period_start=sqlc.arg(expected_start) AND s.period_end=sqlc.arg(expected_end)
AND (SELECT count(*) FROM orders existing WHERE existing.tenant_id=s.tenant_id AND existing.status IN('paid','partially_refunded','refunded'))=1
AND (SELECT count(*) FROM payments receipt WHERE receipt.tenant_id=s.tenant_id)=1
AND NOT EXISTS(SELECT 1 FROM payment_manual_reviews review WHERE review.tenant_id=s.tenant_id AND review.status='open')
AND NOT EXISTS(SELECT 1 FROM refunds r WHERE r.tenant_id=s.tenant_id)
AND NOT EXISTS(SELECT 1 FROM subscription_terms t WHERE t.tenant_id=s.tenant_id)
FOR UPDATE OF s,o,p;

-- name: PlatformRepairLegacySubscriptionPeriod :execrows
UPDATE subscriptions SET period_start=sqlc.arg(corrected_start),period_end=sqlc.arg(corrected_end)
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(subscription_id) AND period_start=sqlc.arg(expected_start) AND period_end=sqlc.arg(expected_end);

-- name: PlatformGetOrderAuthorization :one
SELECT authority_digest FROM billing_order_authorizations WHERE tenant_id=$1 AND order_id=$2;

-- name: PlatformAuthorizeBillingOrder :execrows
INSERT INTO billing_order_authorizations(tenant_id,order_id,authority_digest,authorized_by)
VALUES($1,$2,$3,$4) ON CONFLICT(order_id) DO NOTHING;

-- name: PlatformLockCouponRedemptionForAuthority :one
SELECT coupon_code FROM coupon_redemptions WHERE tenant_id=$1 AND order_id=$2 FOR SHARE;
