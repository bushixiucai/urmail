-- name: GetBillingBusinessTimezone :one
-- scope: platform

-- app-pending: INVALID_TIMEZONE / Local validation → Go callers (aiQuotaLocation).
SELECT COALESCE(
  (SELECT json_extract(value,'$') FROM settings WHERE scope='tenant' AND scope_id=?1 AND key='business_timezone'),
  (SELECT json_extract(s.value,'$') FROM settings s JOIN tenants t ON t.brand_id=s.scope_id WHERE t.id=?1 AND s.scope='brand' AND s.key='business_timezone'),
  (SELECT json_extract(value,'$') FROM settings WHERE scope='platform' AND scope_id=0 AND key='business_timezone'),
  'Asia/Shanghai') AS timezone;

-- name: PlatformLockBillingTenant :exec
-- scope: platform

SELECT pg_advisory_xact_lock(?1);

-- name: MailServiceAllowed :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=?1 AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready'))) AS allowed;
