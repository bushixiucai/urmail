-- name: GetBillingBusinessTimezone :one
-- scope: platform

-- app-pending: INVALID_TIMEZONE / Local validation → Go callers (aiQuotaLocation).
SELECT COALESCE(
  (SELECT json_extract(value,'$') FROM settings WHERE scope='tenant' AND scope_id=?1 AND key='business_timezone'),
  (SELECT json_extract(s.value,'$') FROM settings s JOIN tenants t ON t.brand_id=s.scope_id WHERE t.id=?1 AND s.scope='brand' AND s.key='business_timezone'),
  (SELECT json_extract(value,'$') FROM settings WHERE scope='platform' AND scope_id=0 AND key='business_timezone'),
  'Asia/Shanghai') AS timezone;

-- PlatformLockBillingTenant 在 S5 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- PG 用事务级 advisory lock 串行化租户计费写入；SQLite 改为单写者事务串行
-- （BEGIN IMMEDIATE），详见 scripts/sqlite/lock-semantics.md。


-- name: MailServiceAllowed :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=?1 AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready'))) AS allowed;
