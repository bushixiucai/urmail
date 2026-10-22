-- name: GetChannelByCode :one
SELECT id, brand_id, code, name, kind, commission_bps, parent_id, contact, active, created_at
FROM channels
WHERE code = $1 AND active = true;

-- name: CreateChannelAttribution :one
INSERT INTO channel_attribution (
    tenant_id, channel_id, ref_code
) VALUES (
    $1, $2, $3
) ON CONFLICT (tenant_id) DO NOTHING
RETURNING tenant_id, channel_id, ref_code, created_at;

-- name: GetChannelAttributionByTenant :one
SELECT tenant_id, channel_id, ref_code, created_at
FROM channel_attribution
WHERE tenant_id = $1;

-- name: CreateCommission :one
INSERT INTO commissions (
    channel_id, tenant_id, order_id, amount_cents, bps, status
) VALUES (
    $1, $2, $3, $4, $5, $6
) RETURNING id, channel_id, tenant_id, order_id, amount_cents, bps, status, created_at;
