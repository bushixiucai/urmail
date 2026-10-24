-- Tenant outcomes use the last observation per queue lifecycle/recipient,
-- not the number of log lines or deferred retries.
-- name: TenantDeliveryOutcomeCounts :many
SELECT status,count(*)::bigint AS count FROM (
 SELECT DISTINCT ON(instance,lifecycle_id,recipient) status
 FROM delivery_events
 WHERE tenant_id=$1 AND occurred_at >= $2 AND occurred_at <= @window_end::timestamptz
   AND recipient IS NOT NULL AND status IN('sent','bounced','expired','deferred')
 ORDER BY instance,lifecycle_id,recipient,occurred_at DESC,id DESC
) AS latest GROUP BY status ORDER BY status;
