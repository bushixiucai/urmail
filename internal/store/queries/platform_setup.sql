-- name: PlatformSetupTenant :one
INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,$2,$3,'pending')
ON CONFLICT(brand_id,slug) DO UPDATE SET name=EXCLUDED.name
RETURNING id,(xmax=0)::boolean AS created;

-- name: PlatformGrantSetupFreeTerm :execrows
WITH gift AS (
 UPDATE subscriptions SET status='active',period_start=now(),period_end=sqlc.arg(period_end),plan_snapshot=(SELECT to_jsonb(p) FROM plans p WHERE code='free' AND active)
 WHERE EXISTS(SELECT 1 FROM plans WHERE code='free' AND active) AND tenant_id=sqlc.arg(tenant_id) AND plan_id='free' AND status IN('active','canceled')
 AND NOT EXISTS(SELECT 1 FROM subscriptions paid WHERE paid.tenant_id=sqlc.arg(tenant_id) AND paid.plan_id<>'free' AND paid.status IN('active','trialing','past_due'))
 AND NOT EXISTS(SELECT 1 FROM subscription_terms paid WHERE paid.tenant_id=sqlc.arg(tenant_id) AND paid.revoked_at IS NULL AND paid.period_end>now()) RETURNING tenant_id
)
UPDATE tenants SET status='active' WHERE tenants.id=sqlc.arg(tenant_id) AND EXISTS(SELECT 1 FROM gift WHERE gift.tenant_id=tenants.id);
