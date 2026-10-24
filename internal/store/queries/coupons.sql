-- name: GetCouponByCode :one
SELECT code, kind, value, max_redemptions, redeemed_count, valid_from, valid_to, active
FROM coupons
WHERE code = $1 AND active = true AND (valid_to IS NULL OR valid_to > now()) AND (valid_from IS NULL OR valid_from <= now());

-- name: GetCouponRedemptionForOrder :one
SELECT coupon_code, tenant_id, order_id, created_at
FROM coupon_redemptions
WHERE tenant_id = $1 AND order_id = $2;

-- name: RedeemCoupon :one
SELECT urmail_redeem_coupon(sqlc.arg(code)::text, sqlc.narg(order_id)::bigint)::text AS result;
