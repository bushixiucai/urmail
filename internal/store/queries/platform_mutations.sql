-- Platform mutations share the request's audited transaction.
-- name: PlatformEnsureManagedBrand :one
-- scope: platform

INSERT INTO brands(slug,name) VALUES(?1,?2) ON CONFLICT(slug) DO UPDATE SET slug=EXCLUDED.slug WHERE brands.status='active' RETURNING id;

-- name: PlatformCreateManagedTenant :one
-- scope: platform

INSERT INTO tenants(brand_id,name,slug,status) VALUES(?1,?2,?3,?4) RETURNING id;

-- name: PlatformFindManagedUser :one
-- scope: platform

SELECT id FROM users WHERE email=?1;

-- name: PlatformCreateManagedUser :one
-- scope: platform

INSERT INTO users(email,password_hash,display_name,status) VALUES(?1,?2,?3,'active') RETURNING id;

-- name: PlatformGrantTenantOwner :exec
-- scope: platform

INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(?1,?2,'owner') ON CONFLICT(user_id,tenant_id) DO UPDATE SET role='owner';

-- name: PlatformDemotePreviousOwners :exec
-- scope: platform

UPDATE user_tenant_roles SET role='admin' WHERE tenant_id=?1 AND role='owner' AND user_id<>?2;

-- name: PlatformSetTenantOwner :execrows
-- scope: platform

UPDATE tenants SET owner_user_id=?2 WHERE id=?1;

-- name: PlatformRenameTenant :execrows
-- scope: platform

UPDATE tenants SET name=?2 WHERE id=?1;

