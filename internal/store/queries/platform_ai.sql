-- name: PlatformGetAIProvider :one
SELECT * FROM ai_provider_configs WHERE tenant_id=$1;

-- name: PlatformSetAIProvider :one
INSERT INTO ai_provider_configs(
  tenant_id,mode,provider,endpoint,model,encrypted_key,enabled,local_only,
  platform_provider,platform_endpoint,platform_model,platform_encrypted_key,platform_enabled,
  enterprise_mode,enterprise_provider,enterprise_endpoint,enterprise_model,enterprise_encrypted_key,enterprise_enabled,enterprise_local_only,
  active_service,input_micros_per_million,output_micros_per_million,cost_currency
)
VALUES(
  $1,$2,$3,$4,$5,$6,$7,$8,
  CASE WHEN $2 = 'platform' THEN $3 ELSE 'openai' END,
  CASE WHEN $2 = 'platform' THEN $4 ELSE '' END,
  CASE WHEN $2 = 'platform' THEN $5 ELSE '' END,
  CASE WHEN $2 = 'platform' THEN $6 ELSE '' END,
  CASE WHEN $2 = 'platform' THEN $7 ELSE false END,
  CASE WHEN $2 <> 'platform' THEN $2 ELSE 'byo' END,
  CASE WHEN $2 <> 'platform' THEN $3 ELSE 'openai' END,
  CASE WHEN $2 <> 'platform' THEN $4 ELSE '' END,
  CASE WHEN $2 <> 'platform' THEN $5 ELSE '' END,
  CASE WHEN $2 <> 'platform' THEN $6 ELSE '' END,
  CASE WHEN $2 <> 'platform' THEN $7 ELSE false END,
  CASE WHEN $2 <> 'platform' THEN $8 ELSE true END,
  CASE WHEN $2 = 'platform' THEN 'platform' ELSE 'enterprise' END,
  $9,$10,COALESCE(NULLIF(sqlc.arg(cost_currency)::text,''),'CNY')
)
ON CONFLICT(tenant_id) DO UPDATE SET
  platform_provider = CASE WHEN EXCLUDED.mode = 'platform' THEN EXCLUDED.provider ELSE ai_provider_configs.platform_provider END,
  platform_endpoint = CASE WHEN EXCLUDED.mode = 'platform' THEN EXCLUDED.endpoint ELSE ai_provider_configs.platform_endpoint END,
  platform_model = CASE WHEN EXCLUDED.mode = 'platform' THEN EXCLUDED.model ELSE ai_provider_configs.platform_model END,
  platform_encrypted_key = CASE WHEN EXCLUDED.mode = 'platform' THEN EXCLUDED.encrypted_key ELSE ai_provider_configs.platform_encrypted_key END,
  platform_enabled = CASE WHEN EXCLUDED.mode = 'platform' THEN EXCLUDED.enabled ELSE ai_provider_configs.platform_enabled END,
  enterprise_mode = CASE WHEN EXCLUDED.mode <> 'platform' THEN EXCLUDED.mode ELSE ai_provider_configs.enterprise_mode END,
  enterprise_provider = CASE WHEN EXCLUDED.mode <> 'platform' THEN EXCLUDED.provider ELSE ai_provider_configs.enterprise_provider END,
  enterprise_endpoint = CASE WHEN EXCLUDED.mode <> 'platform' THEN EXCLUDED.endpoint ELSE ai_provider_configs.enterprise_endpoint END,
  enterprise_model = CASE WHEN EXCLUDED.mode <> 'platform' THEN EXCLUDED.model ELSE ai_provider_configs.enterprise_model END,
  enterprise_encrypted_key = CASE WHEN EXCLUDED.mode <> 'platform' THEN EXCLUDED.encrypted_key ELSE ai_provider_configs.enterprise_encrypted_key END,
  enterprise_enabled = CASE WHEN EXCLUDED.mode <> 'platform' THEN EXCLUDED.enabled ELSE ai_provider_configs.enterprise_enabled END,
  enterprise_local_only = CASE WHEN EXCLUDED.mode <> 'platform' THEN EXCLUDED.local_only ELSE ai_provider_configs.enterprise_local_only END,
  active_service = CASE WHEN EXCLUDED.mode = 'platform' THEN 'platform' ELSE 'enterprise' END,
  mode = EXCLUDED.mode,
  provider = EXCLUDED.provider,
  endpoint = EXCLUDED.endpoint,
  model = EXCLUDED.model,
  encrypted_key = EXCLUDED.encrypted_key,
  enabled = EXCLUDED.enabled,
  local_only = EXCLUDED.local_only,
  input_micros_per_million = EXCLUDED.input_micros_per_million,
  output_micros_per_million = EXCLUDED.output_micros_per_million,
  cost_currency = EXCLUDED.cost_currency,
  updated_at = now()
RETURNING *;

-- name: PlatformGetAIRequest :one
SELECT * FROM ai_usage_requests WHERE tenant_id=$1 AND request_key=$2;

-- name: PlatformReserveAIRequest :one
INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,reserved_tokens,reserved_cost_micros,provider,model,cost_currency,input_rate,output_rate,created_at,updated_at)
SELECT sqlc.arg(tenant_id),sqlc.arg(mailbox_id),sqlc.arg(request_key),sqlc.arg(input_digest),sqlc.arg(period_start),sqlc.arg(period_end),sqlc.arg(mode),sqlc.arg(reserved_tokens),sqlc.arg(reserved_cost_micros),sqlc.arg(provider),sqlc.arg(model),sqlc.arg(cost_currency),sqlc.arg(input_rate),sqlc.arg(output_rate),sqlc.arg(accounted_at)::timestamptz,sqlc.arg(accounted_at)::timestamptz
WHERE (NOT sqlc.arg(independent_window)::boolean OR sqlc.arg(mode)::text IN('byo','local'))
AND NOT EXISTS(SELECT 1 FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND ((NOT sqlc.arg(independent_window)::boolean AND period_start=sqlc.arg(period_start)) OR (sqlc.arg(independent_window)::boolean AND mode IN('byo','local') AND created_at>=sqlc.arg(period_start) AND created_at<sqlc.arg(period_end))) AND mode='platform' AND sqlc.arg(mode)='platform' AND cost_currency<>sqlc.arg(cost_currency))
AND (SELECT count(*) FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND ((NOT sqlc.arg(independent_window)::boolean AND period_start=sqlc.arg(period_start)) OR (sqlc.arg(independent_window)::boolean AND mode IN('byo','local') AND created_at>=sqlc.arg(period_start) AND created_at<sqlc.arg(period_end))) AND status<>'released')<sqlc.arg(call_limit)::bigint
AND (SELECT count(*) FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND ((NOT sqlc.arg(independent_window)::boolean AND period_start=sqlc.arg(period_start)) OR (sqlc.arg(independent_window)::boolean AND mode IN('byo','local') AND created_at>=sqlc.arg(period_start) AND created_at<sqlc.arg(period_end))) AND status<>'released')<sqlc.arg(mailbox_call_limit)::bigint
AND (SELECT count(*) FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND status='reserved' AND (NOT sqlc.arg(independent_window)::boolean OR mode IN('byo','local')))<sqlc.arg(concurrency_limit)::bigint
AND (SELECT COALESCE(sum(COALESCE(actual_tokens,reserved_tokens)),0) FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND ((NOT sqlc.arg(independent_window)::boolean AND period_start=sqlc.arg(period_start)) OR (sqlc.arg(independent_window)::boolean AND mode IN('byo','local') AND created_at>=sqlc.arg(period_start) AND created_at<sqlc.arg(period_end))) AND status<>'released')+sqlc.arg(reserved_tokens)::bigint<=sqlc.arg(token_limit)::bigint
AND (SELECT COALESCE(sum(COALESCE(actual_cost_micros,reserved_cost_micros)),0) FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND ((NOT sqlc.arg(independent_window)::boolean AND period_start=sqlc.arg(period_start)) OR (sqlc.arg(independent_window)::boolean AND mode IN('byo','local') AND created_at>=sqlc.arg(period_start) AND created_at<sqlc.arg(period_end))) AND status<>'released')+sqlc.arg(reserved_cost_micros)::bigint<=sqlc.arg(cost_limit)::bigint
RETURNING *;

-- name: PlatformSetAIBudget :exec
UPDATE ai_provider_configs SET daily_token_limit=sqlc.arg(daily_token_limit),daily_cost_micros=sqlc.arg(daily_cost_micros),monthly_token_limit=sqlc.arg(monthly_token_limit),monthly_cost_micros=sqlc.arg(monthly_cost_micros),updated_at=now()
WHERE tenant_id=sqlc.arg(tenant_id);

-- name: PlatformSumAIUsageBetween :one
SELECT COALESCE(sum(COALESCE(actual_tokens,reserved_tokens)),0)::bigint AS tokens,COALESCE(sum(COALESCE(actual_cost_micros,reserved_cost_micros)),0)::bigint AS cost_micros
FROM ai_usage_requests WHERE tenant_id=sqlc.arg(tenant_id) AND status<>'released' AND created_at>=sqlc.arg(window_start) AND created_at<sqlc.arg(window_end);

-- name: PlatformReserveMailboxAIRequest :one
INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,provider,model,cost_currency,input_rate,output_rate,reserved_tokens,reserved_cost_micros,created_at,updated_at)
VALUES(sqlc.arg(tenant_id),sqlc.arg(mailbox_id),sqlc.arg(request_key),sqlc.arg(input_digest),sqlc.arg(period_start),sqlc.arg(period_end),sqlc.arg(mode),sqlc.arg(provider),sqlc.arg(model),sqlc.arg(cost_currency),sqlc.arg(input_rate),sqlc.arg(output_rate),sqlc.arg(reserved_tokens),sqlc.arg(reserved_cost_micros),sqlc.arg(accounted_at)::timestamptz,sqlc.arg(accounted_at)::timestamptz)
RETURNING *;

-- name: PlatformFinishAIRequest :execrows
UPDATE ai_usage_requests SET status=sqlc.arg(status),actual_tokens=sqlc.narg(actual_tokens),actual_cost_micros=sqlc.narg(actual_cost_micros),result=sqlc.narg(result),updated_at=now()
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(id) AND status='reserved';

-- name: PlatformAIUsage :many
SELECT id,mailbox_id,request_key,period_start,period_end,mode,provider,model,cost_currency,status,reserved_tokens,reserved_cost_micros,actual_tokens,actual_cost_micros,created_at FROM ai_usage_requests WHERE tenant_id=$1 ORDER BY id DESC LIMIT 100;

-- name: PlatformGetMailboxSieve :one
SELECT id,tenant_id,address,sieve_script,sieve_mode FROM mailboxes WHERE tenant_id=$1 AND id=$2 AND status='active';

-- name: PlatformSetMailboxSieve :execrows
UPDATE mailboxes SET sieve_script=sqlc.arg(sieve_script) WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(id) AND status='active' AND sieve_mode='managed' AND coalesce(sieve_script,'')=sqlc.arg(expected_script)::text;

-- name: PlatformExpireAIReservations :exec
UPDATE ai_usage_requests SET status='uncertain',updated_at=now() WHERE tenant_id=$1 AND status='reserved' AND updated_at<now()-interval '2 minutes';

-- name: PlatformConfigureTenantAI :exec
SELECT urmail_configure_tenant_ai(sqlc.arg(mode)::text,sqlc.arg(provider)::text,sqlc.arg(endpoint)::text,sqlc.arg(model)::text,sqlc.arg(encrypted_key)::text,sqlc.arg(enabled)::boolean,sqlc.arg(local_only)::boolean);

-- name: PlatformGetSieveActivationTarget :one
SELECT m.id,m.tenant_id,m.address,m.sieve_script,m.sieve_mode FROM mailboxes m
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE m.tenant_id=$1 AND m.id=$2 AND m.status='active' AND t.status='active' AND d.status='active' AND urmail_mail_allowed(m.tenant_id) FOR UPDATE OF m;

-- name: PlatformListSieveScripts :many
SELECT m.id,m.tenant_id,m.sieve_script FROM mailboxes m
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE m.sieve_mode='managed' AND m.sieve_script IS NOT NULL AND m.status='active' AND t.status='active' AND d.status='active' AND urmail_mail_allowed(m.tenant_id);


-- name: PlatformResolveAIUsage :one
UPDATE ai_usage_requests SET status=$3,actual_tokens=$4,actual_cost_micros=$5,review_reason=$6,reviewed_by=$7,reviewed_at=now(),updated_at=now()
WHERE tenant_id=$1 AND id=$2 AND status='uncertain' AND cost_currency=sqlc.arg(cost_currency)
RETURNING id,status,actual_tokens,actual_cost_micros,mode;

-- name: TenantConsentAICloud :exec
SELECT urmail_consent_tenant_ai(@configuration_digest::text,@scope_hash::text,@allow_cloud::boolean);

-- name: AICloudAuthorized :one
SELECT urmail_ai_cloud_authorized(@tenant_id::bigint,@configuration_digest::text,@scope_hash::text)::boolean AS allowed;

-- name: PlatformPendingAIUsage :many
SELECT id,mailbox_id,mode,provider,model,cost_currency,status,reserved_tokens,reserved_cost_micros,actual_tokens,actual_cost_micros,period_start,period_end,updated_at
FROM ai_usage_requests WHERE tenant_id=$1 AND status IN('reserved','uncertain') ORDER BY updated_at,id LIMIT 100;

-- name: PlatformConfigureTenantAIGovernance :exec
SELECT urmail_configure_tenant_ai_governance(sqlc.arg(personal_mode)::text, sqlc.arg(approved_endpoints)::text[]);

-- name: PlatformSetActiveAIService :exec
UPDATE ai_provider_configs SET
  active_service = sqlc.arg(active_service)::text,
  mode = CASE WHEN sqlc.arg(active_service)::text = 'platform' THEN 'platform' ELSE enterprise_mode END,
  provider = CASE WHEN sqlc.arg(active_service)::text = 'platform' THEN platform_provider ELSE enterprise_provider END,
  endpoint = CASE WHEN sqlc.arg(active_service)::text = 'platform' THEN platform_endpoint ELSE enterprise_endpoint END,
  model = CASE WHEN sqlc.arg(active_service)::text = 'platform' THEN platform_model ELSE enterprise_model END,
  encrypted_key = CASE WHEN sqlc.arg(active_service)::text = 'platform' THEN platform_encrypted_key ELSE enterprise_encrypted_key END,
  enabled = CASE WHEN sqlc.arg(active_service)::text = 'platform' THEN platform_enabled ELSE enterprise_enabled END,
  local_only = CASE WHEN sqlc.arg(active_service)::text = 'platform' THEN false ELSE enterprise_local_only END,
  updated_at = now()
WHERE tenant_id = sqlc.arg(tenant_id);