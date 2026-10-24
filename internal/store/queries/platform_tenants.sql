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
SELECT *
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

-- name: PlatformUpdateOrderPaid :execrows
UPDATE orders SET status='paid', paid_at=now() WHERE tenant_id=$1 AND id=$2 AND status='pending';

-- name: PlatformExpireOnboardingTasks :execrows
UPDATE onboarding_tasks SET status='failed', last_error='maximum onboarding attempts exceeded', leased_until=NULL, leased_by=NULL, updated_at=now()
WHERE attempts >= max_attempts AND (status='pending' OR (status='running' AND leased_until < now()));

-- name: PlatformClaimOnboardingTask :many
WITH candidate AS (
    SELECT id FROM onboarding_tasks
    WHERE (status = 'pending' AND attempts < max_attempts)
       OR (status = 'running' AND leased_until < now() AND attempts < max_attempts)
    ORDER BY created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(batch_size)::int
)
UPDATE onboarding_tasks AS task
SET status='running', attempts=task.attempts+1, leased_by=sqlc.arg(worker_id)::text, leased_until=now()+make_interval(secs => sqlc.arg(lease_seconds)::int), updated_at=now()
FROM candidate
WHERE task.id=candidate.id
RETURNING task.id, task.tenant_id, task.domain_id, task.order_id, task.status, task.step, task.attempts, task.max_attempts, task.last_error, task.leased_until, task.leased_by, task.created_at, task.updated_at, task.completed_at;

-- name: PlatformBindOnboardingDomain :execrows
UPDATE onboarding_tasks AS task
SET domain_id=sqlc.arg(domain_id)::bigint, step='dns_propagation', status='pending', attempts=0, leased_until=NULL, leased_by=NULL, last_error=NULL, updated_at=now()
FROM domains AS domain
WHERE task.id=sqlc.arg(task_id)::bigint AND task.tenant_id=domain.tenant_id AND domain.id=sqlc.arg(domain_id)::bigint AND task.status='running' AND task.leased_by=sqlc.arg(worker_id)::text AND task.step='domain_verification' AND task.domain_id IS NULL;

-- name: PlatformCompleteOnboardingTask :execrows
UPDATE onboarding_tasks
SET status='completed', step='ready', completed_at=now(), leased_until=NULL, leased_by=NULL, last_error=NULL, updated_at=now()
WHERE id=sqlc.arg(task_id)::bigint AND status='running' AND leased_by=sqlc.arg(worker_id)::text AND step <> 'domain_verification';

-- name: PlatformAdvanceOnboardingTask :execrows
UPDATE onboarding_tasks
SET status='pending', step=sqlc.arg(step)::text, attempts=0, leased_until=NULL, leased_by=NULL, last_error=NULL, updated_at=now()
WHERE id=sqlc.arg(task_id)::bigint AND status='running' AND leased_by=sqlc.arg(worker_id)::text AND step <> 'domain_verification';

-- name: PlatformFailOnboardingTask :execrows
UPDATE onboarding_tasks
SET status=CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'pending' END, last_error=sqlc.arg(error)::text, leased_until=NULL, leased_by=NULL, updated_at=now()
WHERE id=sqlc.arg(task_id)::bigint AND status='running' AND leased_by=sqlc.arg(worker_id)::text AND step <> 'domain_verification';

-- name: PlatformGetOrderForSettlement :one
SELECT id, tenant_id, order_no, kind, status, amount_cents, currency, plan_code, channel_id, billing_cycle
FROM orders WHERE tenant_id=$1 AND id=$2 FOR UPDATE;

-- name: PlatformGetPaymentByProviderTransaction :one
SELECT id,tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status,raw,created_at,paid_at,account_scope
FROM payments WHERE provider=$1 AND provider_txn_id=$2 AND account_scope=COALESCE(NULLIF(sqlc.arg(account_scope)::text,''),'legacy');

-- name: PlatformGetPaymentEventByProviderAndID :one
SELECT id,provider,provider_event_id,payload,processed_at,account_scope
FROM payment_events WHERE provider=$1 AND provider_event_id=$2 AND account_scope=COALESCE(NULLIF(sqlc.arg(account_scope)::text,''),'legacy');

-- name: PlatformInsertPaymentEventPlaceholder :one
INSERT INTO payment_events(provider,provider_event_id,payload,account_scope) VALUES($1,$2,$3,COALESCE(NULLIF(sqlc.arg(account_scope)::text,''),'legacy'))
ON CONFLICT(provider,account_scope,provider_event_id) DO NOTHING
RETURNING id,provider,provider_event_id,processed_at;

-- name: PlatformRedeemCoupon :one
SELECT urmail_redeem_coupon_for_tenant(sqlc.arg(code)::text, sqlc.arg(tenant_id)::bigint, sqlc.narg(order_id)::bigint)::text AS result;

-- name: PlatformUpsertCoupon :one
INSERT INTO coupons (code, kind, value, max_redemptions, valid_from, valid_to, active,currency)
VALUES ($1, $2, $3, $4, $5, $6, $7,sqlc.narg(currency))
ON CONFLICT (code) DO UPDATE SET
    kind = EXCLUDED.kind,
    value = EXCLUDED.value,
    max_redemptions = EXCLUDED.max_redemptions,
    valid_from = EXCLUDED.valid_from,
    valid_to = EXCLUDED.valid_to,
    active = EXCLUDED.active,
 currency=EXCLUDED.currency
RETURNING *;


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


-- name: PlatformListAuditLogs :many
SELECT id, tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail, created_at
FROM audit_logs
ORDER BY created_at DESC, id DESC
LIMIT $1;

