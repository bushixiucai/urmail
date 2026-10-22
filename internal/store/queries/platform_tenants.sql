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
