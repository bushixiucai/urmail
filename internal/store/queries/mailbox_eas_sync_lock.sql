-- name: LockMailboxEASSync :exec
-- scope: platform

SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(tenant_id) || ':' || sqlc.arg(mailbox_id) || ':' || sqlc.arg(device_id), 421932));
