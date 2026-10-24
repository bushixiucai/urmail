-- name: RedeemTenantTrial :one
SELECT urmail_redeem_trial(sqlc.arg(tenant_id)::bigint,sqlc.arg(domain_id)::bigint,sqlc.arg(coupon_code)::text,sqlc.arg(plan_code)::text)::timestamptz AS ends_at;

-- name: GetTenantTrial :one
SELECT tenant_id,domain_id,coupon_code,plan_code,started_at,ends_at FROM tenant_trials WHERE tenant_id=$1;
