-- name: PlatformLockRecurringAdminActor :one
SELECT u.id FROM users u JOIN platform_admins a ON a.user_id=u.id WHERE u.id=$1 AND u.status='active' FOR SHARE OF u,a;

-- name: PlatformLockRecurringAdminMerchant :one
SELECT * FROM payment_merchants WHERE id=$1 FOR UPDATE;

-- name: PlatformGetRecurringAdminCapability :one
SELECT * FROM payment_recurring_capabilities WHERE merchant_id=$1;

-- name: PlatformLockRecurringAdminCapability :one
SELECT * FROM payment_recurring_capabilities WHERE merchant_id=$1 FOR UPDATE;

-- name: PlatformLockRecurringBindingTarget :one
SELECT s.id FROM subscriptions s JOIN tenants t ON t.id=s.tenant_id
WHERE s.tenant_id=$1 AND s.id=$2 AND s.status IN('active','past_due','trialing') AND t.status='active' FOR SHARE OF s,t;
