-- Platform administration queries (no tenant scope; platform role only).

-- name: PlatformOverview :one
SELECT
    (SELECT count(*) FROM tenants) AS tenants,
    (SELECT count(*) FROM tenants WHERE status = 'active') AS active_tenants,
    (SELECT count(*) FROM users) AS users,
    (SELECT count(*) FROM domains) AS domains,
    (SELECT count(*) FROM mailboxes) AS mailboxes,
    (SELECT count(*) FROM subscriptions WHERE status IN ('active', 'trialing')) AS active_subscriptions;

-- name: PlatformListTenants :many
SELECT t.id,
       t.brand_id,
       t.name,
       t.slug,
       t.status,
       t.owner_user_id,
       t.created_at,
       (SELECT count(*) FROM domains d WHERE d.tenant_id = t.id) AS domain_count,
       (SELECT count(*) FROM mailboxes m WHERE m.tenant_id = t.id) AS mailbox_count
FROM tenants t
ORDER BY t.id ASC;

-- name: PlatformUpdateTenantStatus :one
UPDATE tenants
SET status = $2
WHERE id = $1
RETURNING id, brand_id, name, slug, status, owner_user_id, created_at;

-- name: PlatformListBrands :many
SELECT id, slug, name, logo_url, favicon_url, primary_color, theme, created_at
FROM brands
ORDER BY id ASC;

-- name: PlatformListPlans :many
SELECT code, name, description, price_cents, currency, quota_bytes,
       mailbox_limit, domain_limit, daily_send_limit, features, sort_order, active
FROM plans
ORDER BY sort_order ASC, price_cents ASC;

-- name: PlatformListCoupons :many
SELECT code, kind, value, max_redemptions, redeemed_count, valid_from, valid_to, active
FROM coupons
ORDER BY code ASC;


-- name: PlatformUpsertPlan :one
INSERT INTO plans (
    code, name, description, price_cents, currency, quota_bytes,
    mailbox_limit, domain_limit, daily_send_limit, features, sort_order, active
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
ON CONFLICT (code) DO UPDATE SET
    name = EXCLUDED.name,
    description = EXCLUDED.description,
    price_cents = EXCLUDED.price_cents,
    currency = EXCLUDED.currency,
    quota_bytes = EXCLUDED.quota_bytes,
    mailbox_limit = EXCLUDED.mailbox_limit,
    domain_limit = EXCLUDED.domain_limit,
    daily_send_limit = EXCLUDED.daily_send_limit,
    features = EXCLUDED.features,
    sort_order = EXCLUDED.sort_order,
    active = EXCLUDED.active
RETURNING code, name, description, price_cents, currency, quota_bytes,
          mailbox_limit, domain_limit, daily_send_limit, features, sort_order, active;

-- name: PlatformUpsertCoupon :one
INSERT INTO coupons (code, kind, value, max_redemptions, valid_from, valid_to, active)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (code) DO UPDATE SET
    kind = EXCLUDED.kind,
    value = EXCLUDED.value,
    max_redemptions = EXCLUDED.max_redemptions,
    valid_from = EXCLUDED.valid_from,
    valid_to = EXCLUDED.valid_to,
    active = EXCLUDED.active
RETURNING code, kind, value, max_redemptions, redeemed_count, valid_from, valid_to, active;


-- name: PlatformUpsertBrand :one
INSERT INTO brands (slug, name, theme, logo_url, favicon_url, primary_color, status)
VALUES ($1, $2, $3, $4, $5, $6, 'active')
ON CONFLICT (slug) DO UPDATE SET
    name = EXCLUDED.name,
    theme = EXCLUDED.theme,
    logo_url = EXCLUDED.logo_url,
    favicon_url = EXCLUDED.favicon_url,
    primary_color = EXCLUDED.primary_color,
    status = 'active'
RETURNING id, slug, name, logo_url, favicon_url, primary_color, theme, created_at;

-- name: PlatformListChannels :many
SELECT id, brand_id, code, name, kind, commission_bps, parent_id, contact, active, created_at
FROM channels
ORDER BY code ASC;

-- name: PlatformUpsertChannel :one
INSERT INTO channels (brand_id, code, name, kind, commission_bps, parent_id, contact, active)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (code) DO UPDATE SET
    brand_id = EXCLUDED.brand_id,
    name = EXCLUDED.name,
    kind = EXCLUDED.kind,
    commission_bps = EXCLUDED.commission_bps,
    parent_id = EXCLUDED.parent_id,
    contact = EXCLUDED.contact,
    active = EXCLUDED.active
RETURNING id, brand_id, code, name, kind, commission_bps, parent_id, contact, active, created_at;

