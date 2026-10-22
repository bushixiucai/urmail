-- name: GetCouponByCode :one
SELECT code, kind, value, max_redemptions, redeemed_count, valid_from, valid_to, active
FROM coupons
WHERE code = $1 AND active = true AND (valid_to IS NULL OR valid_to > now()) AND (valid_from IS NULL OR valid_from <= now());

-- name: GetCouponForUpdate :one
-- Locks the coupon row for concurrent redemption serialization
SELECT code, kind, value, max_redemptions, redeemed_count, valid_from, valid_to, active
FROM coupons
WHERE code = $1 AND active = true AND (valid_to IS NULL OR valid_to > now()) AND (valid_from IS NULL OR valid_from <= now())
FOR UPDATE;

-- name: IncrementCouponRedemption :one
UPDATE coupons
SET redeemed_count = redeemed_count + 1
WHERE code = $1 AND (max_redemptions IS NULL OR redeemed_count < max_redemptions)
RETURNING code, kind, value, max_redemptions, redeemed_count, valid_from, valid_to, active;

-- name: CreateCouponRedemption :one
INSERT INTO coupon_redemptions (
    coupon_code, tenant_id, order_id
) VALUES (
    $1, $2, $3
) RETURNING coupon_code, tenant_id, order_id, created_at;
