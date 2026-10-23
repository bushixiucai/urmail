-- name: PlatformUsageMailboxes :many
SELECT id,tenant_id,address,status FROM mailboxes ORDER BY tenant_id,id;

-- name: PlatformRecordMailboxUsage :exec
UPDATE mailboxes SET bytes_used=$3,usage_measured_at=now(),usage_error=NULL WHERE tenant_id=$1 AND id=$2;

-- name: PlatformRecordMailboxUsageError :exec
UPDATE mailboxes SET usage_error='measurement_failed' WHERE tenant_id=$1 AND id=$2;
