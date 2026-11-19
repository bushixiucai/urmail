-- name: GetTenantByID :one
-- scope: platform

SELECT id, brand_id, name, slug, status, owner_user_id, settings, created_at
FROM tenants
WHERE id = ?1;

-- name: GetTenantBySlug :one
-- scope: platform

SELECT id, brand_id, name, slug, status, owner_user_id, settings, created_at
FROM tenants
WHERE brand_id = ?1 AND slug = ?2;

-- name: UpdateTenantStatus :one
-- scope: platform

UPDATE tenants
SET status = ?2
WHERE id = ?1
RETURNING id, brand_id, name, slug, status, owner_user_id, settings, created_at;

-- name: UpdateTenantSettings :one
-- scope: platform

UPDATE tenants
SET settings = ?2
WHERE id = ?1
RETURNING id, brand_id, name, slug, status, owner_user_id, settings, created_at;
