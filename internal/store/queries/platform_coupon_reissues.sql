-- name: PlatformListCouponReissues :many
SELECT * FROM coupons WHERE source_coupon_code IS NOT NULL ORDER BY reissued_at DESC,code LIMIT 100;

-- name: PlatformGetCouponReissue :one
SELECT * FROM coupons WHERE source_coupon_code=$1 AND source_tenant_id=$2;

-- name: PlatformIssueReplacementCoupon :one
INSERT INTO coupons(code,kind,value,max_redemptions,valid_from,valid_to,active,currency,bound_tenant_id,source_coupon_code,source_tenant_id,reissue_reason,reissued_by,reissued_at)
SELECT sqlc.arg(new_code),c.kind,c.value,1,now(),sqlc.arg(valid_until)::timestamptz,true,
 CASE WHEN c.kind='fixed' THEN COALESCE(c.currency,o.currency) ELSE c.currency END,
 r.tenant_id,c.code,r.tenant_id,sqlc.arg(reason),sqlc.arg(actor_id),now()
FROM coupon_redemptions r JOIN coupons c ON c.code=r.coupon_code
LEFT JOIN orders o ON o.tenant_id=r.tenant_id AND o.id=r.order_id
WHERE r.tenant_id=sqlc.arg(tenant_id) AND r.coupon_code=sqlc.arg(source_code)
AND char_length(trim(sqlc.arg(reason)::text)) BETWEEN 10 AND 1000
AND sqlc.arg(valid_until)::timestamptz>now() AND isfinite(sqlc.arg(valid_until)::timestamptz)
AND EXISTS(SELECT 1 FROM platform_admins WHERE user_id=sqlc.arg(actor_id))
AND c.value>0 AND (c.kind<>'percent' OR c.value<=100)
AND (c.kind<>'fixed' OR COALESCE(c.currency,o.currency) IN('CNY','USD'))
AND ((c.kind='trial_days' AND c.value BETWEEN 1 AND 365
 AND NOT EXISTS(SELECT 1 FROM tenant_trials WHERE tenant_id=r.tenant_id)
 AND NOT EXISTS(SELECT 1 FROM subscriptions WHERE tenant_id=r.tenant_id AND plan_id<>'free' AND status IN('active','trialing','past_due') AND period_end>now())
 AND NOT EXISTS(SELECT 1 FROM subscription_terms WHERE tenant_id=r.tenant_id AND period_end>now()))
 OR (c.kind IN('percent','fixed') AND EXISTS(SELECT 1 FROM refunds f WHERE f.tenant_id=r.tenant_id AND f.order_id=r.order_id AND f.status='completed')))
ON CONFLICT(source_coupon_code,source_tenant_id) WHERE source_coupon_code IS NOT NULL DO NOTHING RETURNING *;
