-- name: PlatformGetAIProvider :one
-- scope: platform

SELECT * FROM ai_provider_configs WHERE tenant_id=?1;

-- PlatformSetAIProvider 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- 同语句 + configuration_digest 重算（守卫触发器的 sha256 机械部分）；平台/企业快照与基础列同写

-- name: PlatformGetAIRequest :one
-- scope: platform

SELECT * FROM ai_usage_requests WHERE tenant_id=?1 AND request_key=?2;

-- name: PlatformReserveAIRequest :one
-- scope: platform

INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,reserved_tokens,reserved_cost_micros,provider,model,cost_currency,input_rate,output_rate,created_at,updated_at)
SELECT sqlc.arg(tenant_id),sqlc.arg(mailbox_id),sqlc.arg(request_key),sqlc.arg(input_digest),sqlc.arg(period_start),sqlc.arg(period_end),sqlc.arg(mode),sqlc.arg(reserved_tokens),sqlc.arg(reserved_cost_micros),sqlc.arg(provider),sqlc.arg(model),sqlc.arg(cost_currency),sqlc.arg(input_rate),sqlc.arg(output_rate),sqlc.arg(accounted_at),sqlc.arg(accounted_at)
WHERE (NOT sqlc.arg(independent_window) OR sqlc.arg(mode) IN('byo','local'))
AND NOT EXISTS(SELECT 1 FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND ((NOT sqlc.arg(independent_window) AND period_start=sqlc.arg(period_start)) OR (sqlc.arg(independent_window) AND mode IN('byo','local') AND created_at>=sqlc.arg(period_start) AND created_at<sqlc.arg(period_end))) AND mode='platform' AND sqlc.arg(mode)='platform' AND cost_currency<>sqlc.arg(cost_currency))
AND (SELECT count(*) FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND ((NOT sqlc.arg(independent_window) AND period_start=sqlc.arg(period_start)) OR (sqlc.arg(independent_window) AND mode IN('byo','local') AND created_at>=sqlc.arg(period_start) AND created_at<sqlc.arg(period_end))) AND status<>'released')<sqlc.arg(call_limit)
AND (SELECT count(*) FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND ((NOT sqlc.arg(independent_window) AND period_start=sqlc.arg(period_start)) OR (sqlc.arg(independent_window) AND mode IN('byo','local') AND created_at>=sqlc.arg(period_start) AND created_at<sqlc.arg(period_end))) AND status<>'released')<sqlc.arg(mailbox_call_limit)
AND (SELECT count(*) FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND status='reserved' AND (NOT sqlc.arg(independent_window) OR mode IN('byo','local')))<sqlc.arg(concurrency_limit)
AND (SELECT COALESCE(sum(COALESCE(actual_tokens,reserved_tokens)),0) FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND ((NOT sqlc.arg(independent_window) AND period_start=sqlc.arg(period_start)) OR (sqlc.arg(independent_window) AND mode IN('byo','local') AND created_at>=sqlc.arg(period_start) AND created_at<sqlc.arg(period_end))) AND status<>'released')+sqlc.arg(reserved_tokens)<=sqlc.arg(token_limit)
AND (SELECT COALESCE(sum(COALESCE(actual_cost_micros,reserved_cost_micros)),0) FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND ((NOT sqlc.arg(independent_window) AND period_start=sqlc.arg(period_start)) OR (sqlc.arg(independent_window) AND mode IN('byo','local') AND created_at>=sqlc.arg(period_start) AND created_at<sqlc.arg(period_end))) AND status<>'released')+sqlc.arg(reserved_cost_micros)<=sqlc.arg(cost_limit)
RETURNING *;

-- name: PlatformSetAIBudget :exec
-- scope: platform

UPDATE ai_provider_configs SET daily_token_limit=sqlc.arg(daily_token_limit),daily_cost_micros=sqlc.arg(daily_cost_micros),monthly_token_limit=sqlc.arg(monthly_token_limit),monthly_cost_micros=sqlc.arg(monthly_cost_micros),updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=sqlc.arg(tenant_id);

-- name: PlatformSumAIUsageBetween :one
-- scope: platform

SELECT COALESCE(sum(COALESCE(actual_tokens,reserved_tokens)),0) AS tokens,COALESCE(sum(COALESCE(actual_cost_micros,reserved_cost_micros)),0) AS cost_micros
FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND status<>'released' AND created_at>=sqlc.arg(window_start) AND created_at<sqlc.arg(window_end);

-- name: PlatformReserveMailboxAIRequest :one
-- scope: platform

INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,provider,model,cost_currency,input_rate,output_rate,reserved_tokens,reserved_cost_micros,created_at,updated_at)
VALUES(sqlc.arg(tenant_id),sqlc.arg(mailbox_id),sqlc.arg(request_key),sqlc.arg(input_digest),sqlc.arg(period_start),sqlc.arg(period_end),sqlc.arg(mode),sqlc.arg(provider),sqlc.arg(model),sqlc.arg(cost_currency),sqlc.arg(input_rate),sqlc.arg(output_rate),sqlc.arg(reserved_tokens),sqlc.arg(reserved_cost_micros),sqlc.arg(accounted_at),sqlc.arg(accounted_at))
RETURNING *;

-- name: PlatformFinishAIRequest :execrows
-- scope: platform

UPDATE ai_usage_requests SET status=sqlc.arg(status),actual_tokens=sqlc.narg(actual_tokens),actual_cost_micros=sqlc.narg(actual_cost_micros),result=sqlc.narg(result),updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(id) AND status='reserved';

-- name: PlatformAIUsage :many
-- scope: platform

SELECT id,mailbox_id,request_key,period_start,period_end,mode,provider,model,cost_currency,status,reserved_tokens,reserved_cost_micros,actual_tokens,actual_cost_micros,created_at FROM ai_usage_requests WHERE tenant_id=?1 ORDER BY id DESC LIMIT 100;

-- name: PlatformGetMailboxSieve :one
-- scope: platform

SELECT id,tenant_id,address,sieve_script,sieve_mode FROM mailboxes WHERE tenant_id=?1 AND id=?2 AND status='active';

-- name: PlatformSetMailboxSieve :execrows
-- scope: platform

UPDATE mailboxes SET sieve_script=sqlc.arg(sieve_script) WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(id) AND status='active' AND sieve_mode='managed' AND coalesce(sieve_script,'')=sqlc.arg(expected_script);

-- name: PlatformExpireAIReservations :exec
-- scope: platform

UPDATE ai_usage_requests SET status='uncertain',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE tenant_id=?1 AND status='reserved' AND updated_at<(CAST(strftime('%s','now') AS INTEGER)*1000000)-120000000;

-- PlatformConfigureTenantAI 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- urmail_configure_tenant_ai = 租户/用户会话变量（owner|admin）+ 模式/提供方/local_only 组合校验 + enterprise 快照与基础列同写（空 key 沿用旧企业密钥）

-- name: PlatformGetSieveActivationTarget :one
-- scope: platform

SELECT m.id,m.tenant_id,m.address,m.sieve_script,m.sieve_mode FROM mailboxes m
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE m.tenant_id=?1 AND m.id=?2 AND m.status='active' AND t.status='active' AND d.status='active' AND EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=m.tenant_id AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready')));

-- name: PlatformListSieveScripts :many
-- scope: platform

SELECT m.id,m.tenant_id,m.sieve_script FROM mailboxes m
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE m.sieve_mode='managed' AND m.sieve_script IS NOT NULL AND m.status='active' AND t.status='active' AND d.status='active' AND EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=m.tenant_id AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready')));


-- name: PlatformResolveAIUsage :one
-- scope: platform

UPDATE ai_usage_requests SET status=?3,actual_tokens=?4,actual_cost_micros=?5,review_reason=?6,reviewed_by=?7,reviewed_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND id=?2 AND status='uncertain' AND cost_currency=sqlc.arg(cost_currency)
RETURNING id,status,actual_tokens,actual_cost_micros,mode;

-- TenantConsentAICloud 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- urmail_consent_tenant_ai = 租户/用户会话变量（owner|admin）+ 配置存在 + 授权时必须引用现状 digest/scope_hash 且非 ollama 已启用 + 本地优先开关与同意时间戳

-- name: AICloudAuthorized :one
-- scope: platform

-- app-pending: role/current_setting tenant-context gate → S2 decorator.
SELECT EXISTS(
 SELECT 1 FROM ai_provider_configs c
 JOIN user_tenant_roles r ON r.tenant_id=c.tenant_id AND r.user_id=c.cloud_consent_by
 WHERE c.tenant_id=@tenant_id AND c.enabled AND NOT c.tenant_local_only AND NOT c.local_only
  AND c.provider IN('openai','azure') AND c.configuration_digest=@configuration_digest
  AND c.cloud_scope_hash=@scope_hash AND c.cloud_consent_digest=c.configuration_digest
  AND c.cloud_consent_scope_hash=c.cloud_scope_hash AND c.cloud_consent_at IS NOT NULL
  AND c.cloud_consent_revoked_at IS NULL AND r.role IN('owner','admin')) AS allowed;

-- name: PlatformPendingAIUsage :many
-- scope: platform

SELECT id,mailbox_id,mode,provider,model,cost_currency,status,reserved_tokens,reserved_cost_micros,actual_tokens,actual_cost_micros,period_start,period_end,updated_at
FROM ai_usage_requests WHERE tenant_id=?1 AND status IN('reserved','uncertain') ORDER BY updated_at,id LIMIT 100;

-- PlatformConfigureTenantAIGovernance 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- urmail_configure_tenant_ai_governance = 租户/用户会话变量（owner|admin）+ personal_mode 白名单 + approved_endpoints（PG text[] → SQLite JSON 文本）

-- PlatformSetActiveAIService 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- 同语句 + configuration_digest 重算：切换服务会改写 mode/provider/endpoint/model，旧云同意必须随之失效
