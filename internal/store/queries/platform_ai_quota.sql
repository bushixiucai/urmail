-- name: PlatformSetNonplatformAIQuota :exec
UPDATE ai_provider_configs SET nonplatform_call_limit=$2,nonplatform_mailbox_call_limit=$3,
 nonplatform_concurrency_limit=$4,quota_timezone=$5,updated_at=statement_timestamp()
WHERE tenant_id=$1;

-- name: PlatformAIQuotaUsage :one
WITH window_usage AS MATERIALIZED (
 SELECT mailbox_id FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND status<>'released'
 AND ((NOT sqlc.arg(independent_window)::boolean AND period_start=sqlc.arg(period_start)::timestamptz) OR
 (sqlc.arg(independent_window)::boolean AND mode IN('byo','local') AND created_at>=sqlc.arg(period_start)::timestamptz AND created_at<sqlc.arg(period_end)::timestamptz))
)
SELECT (SELECT count(*) FROM window_usage)::bigint AS calls_used,
 (SELECT count(*) FROM window_usage WHERE mailbox_id=sqlc.arg(mailbox_id)::bigint)::bigint AS mailbox_calls_used,
 (SELECT count(*) FROM ai_usage_requests active WHERE active.tenant_id=sqlc.arg(tenant_id) AND active.status='reserved'
  AND (NOT sqlc.arg(independent_window)::boolean OR active.mode IN('byo','local')))::bigint AS concurrent_reserved;

-- name: PlatformLockAIQuotaTenantReader :one
SELECT u.id FROM users u JOIN user_tenant_roles r ON r.user_id=u.id
WHERE r.tenant_id=$1 AND u.id=$2 AND u.status='active' AND r.role IN('owner','admin') FOR SHARE OF u,r;
