-- name: GetCouponByCode :one
SELECT *
FROM coupons
WHERE code = $1 AND (bound_tenant_id IS NULL OR bound_tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint) AND active = true AND (valid_to IS NULL OR valid_to > now()) AND (valid_from IS NULL OR valid_from <= now());

-- name: GetCouponRedemptionForOrder :one
SELECT coupon_code, tenant_id, order_id, created_at
FROM coupon_redemptions
WHERE tenant_id = $1 AND order_id = $2;

-- name: RedeemCoupon :one
SELECT urmail_redeem_coupon(sqlc.arg(code)::text, sqlc.narg(order_id)::bigint)::text AS result;
