-- Platform administration queries (no tenant scope; platform role only).

-- name: PlatformOverview :one
-- scope: platform

SELECT
    (SELECT count(*) FROM tenants) AS tenants,
    (SELECT count(*) FROM tenants WHERE status = 'active') AS active_tenants,
    (SELECT count(*) FROM users) AS users,
    (SELECT count(*) FROM domains) AS domains,
    (SELECT count(*) FROM mailboxes) AS mailboxes;

-- name: PlatformListTenants :many
-- scope: platform

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
-- scope: platform

UPDATE tenants
SET status = ?2
WHERE id = ?1
RETURNING id, brand_id, name, slug, status, owner_user_id, created_at;

-- name: PlatformListBrands :many
-- scope: platform

SELECT id, slug, name, logo_url, favicon_url, primary_color, theme, created_at
FROM brands
ORDER BY id ASC;

-- name: PlatformExpireOnboardingTasks :execrows
-- scope: platform

UPDATE onboarding_tasks SET status='failed', last_error='maximum onboarding attempts exceeded', leased_until=NULL, leased_by=NULL, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE attempts >= max_attempts AND (status='pending' OR (status='running' AND leased_until < (CAST(strftime('%s','now') AS INTEGER)*1000000)));

-- name: PlatformClaimOnboardingTask :many
-- scope: platform

WITH candidate AS (
    SELECT id FROM onboarding_tasks
    WHERE (status = 'pending' AND attempts < max_attempts)
       OR (status = 'running' AND leased_until < (CAST(strftime('%s','now') AS INTEGER)*1000000) AND attempts < max_attempts)
    ORDER BY created_at, id
    LIMIT sqlc.arg(batch_size)
)
UPDATE onboarding_tasks AS task
SET status='running', attempts=task.attempts+1, leased_by=sqlc.arg(worker_id), leased_until=(CAST(strftime('%s','now') AS INTEGER)*1000000)+make_interval(secs => sqlc.arg(lease_seconds)), updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
FROM candidate
WHERE task.id=candidate.id
RETURNING task.id, task.tenant_id, task.domain_id, task.order_id, task.status, task.step, task.attempts, task.max_attempts, task.last_error, task.leased_until, task.leased_by, task.created_at, task.updated_at, task.completed_at;

-- name: PlatformBindOnboardingDomain :execrows
-- scope: platform

UPDATE onboarding_tasks AS task
SET domain_id=sqlc.arg(domain_id), step='dns_propagation', status='pending', attempts=0, leased_until=NULL, leased_by=NULL, last_error=NULL, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
FROM domains AS domain
WHERE task.id=sqlc.arg(task_id) AND task.tenant_id=domain.tenant_id AND domain.id=sqlc.arg(domain_id) AND task.status='running' AND task.leased_by=sqlc.arg(worker_id) AND task.step='domain_verification' AND task.domain_id IS NULL;

-- name: PlatformCompleteOnboardingTask :execrows
-- scope: platform

UPDATE onboarding_tasks
SET status='completed', step='ready', completed_at=(CAST(strftime('%s','now') AS INTEGER)*1000000), leased_until=NULL, leased_by=NULL, last_error=NULL, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE id=sqlc.arg(task_id) AND status='running' AND leased_by=sqlc.arg(worker_id) AND step <> 'domain_verification';

-- name: PlatformAdvanceOnboardingTask :execrows
-- scope: platform

UPDATE onboarding_tasks
SET status='pending', step=sqlc.arg(step), attempts=0, leased_until=NULL, leased_by=NULL, last_error=NULL, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE id=sqlc.arg(task_id) AND status='running' AND leased_by=sqlc.arg(worker_id) AND step <> 'domain_verification';

-- name: PlatformFailOnboardingTask :execrows
-- scope: platform

UPDATE onboarding_tasks
SET status=CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'pending' END, last_error=sqlc.arg(error), leased_until=NULL, leased_by=NULL, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE id=sqlc.arg(task_id) AND status='running' AND leased_by=sqlc.arg(worker_id) AND step <> 'domain_verification';

-- name: PlatformUpsertBrand :one
-- scope: platform

INSERT INTO brands (slug, name, theme, logo_url, favicon_url, primary_color, status)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, 'active')
ON CONFLICT (slug) DO UPDATE SET
    name = EXCLUDED.name,
    theme = EXCLUDED.theme,
    logo_url = EXCLUDED.logo_url,
    favicon_url = EXCLUDED.favicon_url,
    primary_color = EXCLUDED.primary_color,
    status = 'active'
RETURNING id, slug, name, logo_url, favicon_url, primary_color, theme, created_at;

-- name: PlatformListChannels :many
-- scope: platform

SELECT id, brand_id, code, name, kind, commission_bps, parent_id, contact, active, created_at
FROM channels
ORDER BY code ASC;

-- name: PlatformUpsertChannel :one
-- scope: platform

INSERT INTO channels (brand_id, code, name, kind, commission_bps, parent_id, contact, active)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)
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
-- scope: platform

SELECT id, tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail, created_at
FROM audit_logs
ORDER BY created_at DESC, id DESC
LIMIT ?1;

