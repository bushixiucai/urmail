-- Tenant outcomes use the last observation per queue lifecycle/recipient,
-- not the number of log lines or deferred retries.
-- name: TenantDeliveryOutcomeCounts :many
-- scope: tenant

SELECT status,count(*) AS count FROM (
 SELECT status, ROW_NUMBER() OVER (
   PARTITION BY instance,lifecycle_id,recipient
   ORDER BY occurred_at DESC,id DESC) AS rn
 FROM delivery_events
 WHERE tenant_id=?1 AND occurred_at >= ?2 AND occurred_at <= @window_end
   AND recipient IS NOT NULL AND status IN('sent','bounced','expired','deferred')
) AS latest WHERE rn=1 GROUP BY status ORDER BY status;
