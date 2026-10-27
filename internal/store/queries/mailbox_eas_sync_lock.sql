-- name: LockMailboxEASSync :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(tenant_id)::text || ':' || sqlc.arg(mailbox_id)::text || ':' || sqlc.arg(device_id)::text, 421932));
