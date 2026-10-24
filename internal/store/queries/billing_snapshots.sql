-- name: GetConfiguredPlanPrice :one
SELECT plan_code,currency,billing_cycle,amount_cents,version FROM plan_prices
WHERE plan_code=$1 AND currency=$2 AND billing_cycle=$3;

-- name: SaveBillingQuote :exec
INSERT INTO billing_quotes(digest,tenant_id,payload,plan_snapshot,expires_at,document_snapshot)
VALUES($1,$2,$3,$4,$5,COALESCE(sqlc.arg(document_snapshot)::jsonb,'{}'::jsonb)) ON CONFLICT(digest) DO NOTHING;

-- name: GetBillingQuote :one
SELECT digest,payload,plan_snapshot,expires_at,document_snapshot FROM billing_quotes WHERE tenant_id=$1 AND digest=$2;

-- name: BindOrderQuote :execrows
UPDATE orders SET quote_digest=$3 WHERE tenant_id=$1 AND id=$2 AND quote_digest IS NULL;

-- name: GetOrderQuoteDigest :one
SELECT quote_digest FROM orders WHERE tenant_id=$1 AND id=$2;

-- name: GetEffectivePlan :one
SELECT code,name,description,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features,sort_order,active
FROM urmail_effective_plan($1);

-- name: ListPlanPrices :many
SELECT plan_code,currency,billing_cycle,amount_cents,version FROM plan_prices
WHERE plan_code=$1 ORDER BY currency,billing_cycle;

-- name: GetCurrentSubscriptionTerm :one
SELECT order_id,basis_order_id,period_start,period_end,billing_period_start,base_amount_cents,currency,billing_cycle,plan_snapshot,calendar_snapshot
FROM subscription_terms WHERE tenant_id=$1 AND period_start<=now() AND period_end>now() AND (revoked_at IS NULL OR revoked_at>now())
ORDER BY period_start DESC,order_id DESC LIMIT 1;

-- name: CurrentPaidEntitlementAllowed :one
SELECT NOT EXISTS(SELECT 1 FROM subscription_terms a WHERE a.tenant_id=sqlc.arg(tenant_id)::bigint)
OR EXISTS(SELECT 1 FROM subscription_terms b WHERE b.tenant_id=sqlc.arg(tenant_id)::bigint AND b.period_start<=now() AND b.period_end>now() AND (b.revoked_at IS NULL OR b.revoked_at>now()))
OR COALESCE(urmail_legacy_current_entitlement_end(sqlc.arg(tenant_id)::bigint)>now(),false);

-- name: PendingRefundForTerm :one
SELECT EXISTS(SELECT 1 FROM refunds WHERE tenant_id=$1 AND target_term_order_id=$2 AND entitlement_action<>'none' AND status IN('approved','processing','uncertain'));

-- name: PlanSnapshotFitsResources :one
SELECT urmail_plan_snapshot_fits(sqlc.arg(tenant_id)::bigint,sqlc.arg(plan_snapshot)::jsonb)::boolean AS fits;

-- name: MailServiceAllowed :one
SELECT urmail_mail_allowed($1::bigint)::boolean AS allowed;

-- name: LastFuturePaidPlan :one
SELECT COALESCE(plan_snapshot->>'code','')::text AS plan_code,period_start,period_end FROM subscription_terms
WHERE tenant_id=$1 AND basis_order_id IS NULL AND period_start>now() AND revoked_at IS NULL
ORDER BY period_end DESC,order_id DESC LIMIT 1;
