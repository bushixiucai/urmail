-- name: PlatformSetupTenant :one
INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,$2,$3,'pending')
ON CONFLICT(brand_id,slug) DO UPDATE SET name=EXCLUDED.name
RETURNING id,(xmax=0)::boolean AS created;

