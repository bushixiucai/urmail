-- Platform queries for brand theming. Brand rows and the tenant -> brand
-- relation are not tenant-scoped data, so these are exempt from the tenant_id
-- predicate per AGENTS.md (platform_*.sql).

-- name: PlatformGetBrandThemeByTenant :one
-- Effective branding source row for a tenant's brand.
SELECT b.id AS brand_id, b.slug AS brand_slug, b.theme, b.logo_url, b.favicon_url, b.primary_color
FROM brands b
JOIN tenants t ON t.brand_id = b.id
WHERE t.id = $1;

-- name: PlatformListBrandThemes :many
SELECT id AS brand_id, slug AS brand_slug, theme, logo_url, favicon_url, primary_color
FROM brands
ORDER BY id ASC;
