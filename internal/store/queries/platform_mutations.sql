-- Platform mutations share the request's audited transaction.
-- name: PlatformEnsureManagedBrand :one
INSERT INTO brands(slug,name) VALUES($1,$2) ON CONFLICT(slug) DO UPDATE SET slug=EXCLUDED.slug WHERE brands.status='active' RETURNING id;

-- name: PlatformCreateManagedTenant :one
INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,$2,$3,$4) RETURNING id;

-- name: PlatformFindManagedUser :one
SELECT id FROM users WHERE email=$1;

-- name: PlatformCreateManagedUser :one
INSERT INTO users(email,password_hash,display_name,status) VALUES($1,$2,$3,'active') RETURNING id;

-- name: PlatformGrantTenantOwner :exec
INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner') ON CONFLICT(user_id,tenant_id) DO UPDATE SET role='owner';

-- name: PlatformDemotePreviousOwners :exec
UPDATE user_tenant_roles SET role='admin' WHERE tenant_id=$1 AND role='owner' AND user_id<>$2;

-- name: PlatformSetTenantOwner :execrows
UPDATE tenants SET owner_user_id=$2 WHERE id=$1;

-- name: PlatformRenameTenant :execrows
UPDATE tenants SET name=$2 WHERE id=$1;

-- name: PlatformManagedPlanAvailable :one
SELECT EXISTS(SELECT 1 FROM plans WHERE code=$1 AND active)::boolean AS available;

-- name: PlatformGrantManagedSubscription :exec
INSERT INTO subscriptions(tenant_id,plan_id,status,period_start,period_end) VALUES($1,$2,'active',now(),$3);

-- name: PlatformCreateManagedInvoice :one
INSERT INTO invoices(tenant_id,number,period_start,period_end,amount_cents,currency,status,issued_at) VALUES($1,$2,$3,$4,$5,$6,'issued',now()) RETURNING id;

-- name: PlatformMarkManagedInvoicePaid :execrows
UPDATE invoices SET status='paid' WHERE id=$1 AND status IN('draft','issued');

-- name: PlatformVoidManagedInvoice :execrows
UPDATE invoices SET status='void' WHERE id=$1 AND status IN('draft','issued');

-- name: PlatformSetManagedFreeTerm :exec
UPDATE subscriptions SET period_start=now(),period_end=$2,status='active' WHERE tenant_id=$1 AND plan_id='free' AND status='active';
