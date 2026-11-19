-- name: GetBillingBusinessTimezone :one
-- scope: platform

SELECT urmail_business_timezone(?1) AS timezone;

-- name: PlatformLockBillingTenant :exec
-- scope: platform

SELECT pg_advisory_xact_lock(?1);

-- name: MailServiceAllowed :one
-- scope: platform

SELECT urmail_mail_allowed(?1) AS allowed;
