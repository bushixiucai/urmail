-- name: PublicBrandByHost :one
-- scope: platform

SELECT b.id,b.slug,b.name,b.theme,b.logo_url,b.favicon_url,b.primary_color
FROM brands b JOIN brand_hosts h ON h.brand_id=b.id
WHERE h.hostname=?1 AND b.status='active' AND h.status='active';

-- name: PublicBrandBySlug :one
-- scope: platform

SELECT id,slug,name,theme,logo_url,favicon_url,primary_color
FROM brands WHERE slug=?1 AND status='active';

-- name: PlatformListBrandHosts :many
-- scope: platform

SELECT hostname,brand_id,status,failure_reason,verified_at,cert_issued_at,activated_at,created_at,updated_at FROM brand_hosts ORDER BY hostname;

-- name: PlatformBindBrandHost :one
-- scope: platform

INSERT INTO brand_hosts(hostname,brand_id)
SELECT ?1,b.id FROM brands b WHERE b.id=?2 AND b.status='active'
ON CONFLICT(hostname) DO UPDATE SET brand_id=EXCLUDED.brand_id,
status='pending-verify',failure_reason=NULL,verified_at=NULL,
cert_issued_at=NULL,activated_at=NULL,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE brand_hosts.status='failed' AND brand_hosts.failure_reason='revoked'
RETURNING hostname,brand_id,status,failure_reason,verified_at,cert_issued_at,activated_at,created_at,updated_at;

-- name: PlatformUnbindBrandHostDeleteProof :exec
-- scope: platform

DELETE FROM brand_host_challenges WHERE hostname=sqlc.arg(hostname);

-- name: PlatformUnbindBrandHostMark :exec
-- scope: platform

UPDATE brand_hosts SET status='failed', failure_reason='revoked', verified_at=NULL,
 cert_issued_at=NULL, activated_at=NULL, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE hostname=sqlc.arg(hostname);
-- name: PlatformSetBrandStatus :execrows
-- scope: platform

UPDATE brands SET status=?2 WHERE id=?1 AND slug<>'default';

-- name: PlatformDeleteBrand :execrows
-- scope: platform

DELETE FROM brands WHERE id=?1 AND slug<>'default';

-- name: PlatformManagedBrands :many
-- scope: platform

SELECT id,slug,name,theme,logo_url,favicon_url,primary_color,status,created_at
FROM brands ORDER BY id;

-- name: PlatformSaveBrand :one
-- scope: platform

INSERT INTO brands(slug,name,theme,logo_url,favicon_url,primary_color,status)
VALUES(?1,?2,?3,?4,?5,?6,?7)
ON CONFLICT(slug) DO UPDATE SET name=EXCLUDED.name,theme=EXCLUDED.theme,
logo_url=EXCLUDED.logo_url,favicon_url=EXCLUDED.favicon_url,
primary_color=EXCLUDED.primary_color,status=EXCLUDED.status
RETURNING id,slug,name,theme,logo_url,favicon_url,primary_color,status,created_at;

-- name: PlatformResetBrandThemeClearSettings :exec
-- scope: platform

DELETE FROM settings WHERE scope='brand' AND scope_id=?1 AND key='theme';

-- name: PlatformResetBrandThemeMark :exec
-- scope: platform

UPDATE brands SET theme='{}', logo_url=NULL, favicon_url=NULL, primary_color=NULL WHERE id=?1;
-- name: PlatformPublicThemeSettings :many
-- scope: platform

SELECT scope,scope_id,key,value FROM settings WHERE key='theme'
AND ((scope='platform' AND scope_id=0) OR (scope='brand' AND scope_id=?1));
