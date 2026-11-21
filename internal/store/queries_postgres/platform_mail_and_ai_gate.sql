-- name: GetBillingBusinessTimezone :one
SELECT urmail_business_timezone($1::bigint)::text AS timezone;

-- name: PlatformLockBillingTenant :exec
SELECT pg_advisory_xact_lock($1::bigint);

-- name: MailServiceAllowed :one
SELECT urmail_mail_allowed($1::bigint)::boolean AS allowed;
