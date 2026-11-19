-- name: GetBillingBusinessTimezone :one
-- scope: platform

SELECT urmail_business_timezone(?1) AS timezone;

-- name: PlatformLockBillingTenant :exec
-- scope: platform

SELECT pg_advisory_xact_lock(?1);

-- name: MailServiceAllowed :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=?1 AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready'))) AS allowed;
