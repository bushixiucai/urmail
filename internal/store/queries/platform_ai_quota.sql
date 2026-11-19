-- name: PlatformSetNonplatformAIQuota :exec
-- scope: platform

UPDATE ai_provider_configs SET nonplatform_call_limit=?2,nonplatform_mailbox_call_limit=?3,
 nonplatform_concurrency_limit=?4,quota_timezone=?5,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1;

-- name: PlatformAIQuotaUsage :one
-- scope: platform

WITH window_usage AS MATERIALIZED (
 SELECT mailbox_id FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND status<>'released'
 AND ((NOT sqlc.arg(independent_window) AND period_start=sqlc.arg(period_start)) OR
 (sqlc.arg(independent_window) AND mode IN('byo','local') AND created_at>=sqlc.arg(period_start) AND created_at<sqlc.arg(period_end)))
)
SELECT (SELECT count(*) FROM window_usage) AS calls_used,
 (SELECT count(*) FROM window_usage WHERE mailbox_id=sqlc.arg(mailbox_id)) AS mailbox_calls_used,
 (SELECT count(*) FROM ai_usage_requests active WHERE active.tenant_id=sqlc.arg(tenant_id) AND active.status='reserved'
  AND (NOT sqlc.arg(independent_window) OR active.mode IN('byo','local'))) AS concurrent_reserved;

-- name: PlatformLockAIQuotaTenantReader :one
-- scope: platform

SELECT u.id FROM users u JOIN user_tenant_roles r ON r.user_id=u.id
WHERE r.tenant_id=?1 AND u.id=?2 AND u.status='active' AND r.role IN('owner','admin');

-- name: PlatformLockPlatformAdmin :one
-- scope: platform

SELECT u.id FROM users u JOIN platform_admins a ON a.user_id=u.id WHERE u.id=?1 AND u.status='active';
