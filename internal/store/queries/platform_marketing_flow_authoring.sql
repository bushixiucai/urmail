-- name: PlatformMarketingFlowIsVersioned :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM marketing_flow_versions v WHERE v.tenant_id=?1 AND v.runtime_flow_id=?2) AS versioned;

-- name: PlatformGetMarketingFlowSnapshotContent :one
-- scope: platform

SELECT subject,text_body FROM marketing_flow_content_snapshots WHERE tenant_id=?1 AND runtime_flow_id=?2 AND step_key=?3;

-- name: PlatformCheckMarketingFlowLease :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM marketing_flow_positions p JOIN marketing_flows f ON f.tenant_id=p.tenant_id AND f.id=p.flow_id
WHERE p.tenant_id=?1 AND p.flow_id=?2 AND p.email=?3 AND p.lease_owner=?4 AND p.lease_fence=?5 AND p.lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND p.stopped_at IS NULL AND f.status='active') AS allowed;

-- name: PlatformVersionedMarketingFlowMayAdvance :one
-- scope: platform

SELECT CASE WHEN EXISTS(SELECT 1 FROM marketing_flow_versions v WHERE v.tenant_id=?1 AND v.runtime_flow_id=?2)
THEN EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=?1 AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready')))
AND EXISTS(SELECT 1 FROM marketing_flow_positions p JOIN marketing_flows f ON f.tenant_id=p.tenant_id AND f.id=p.flow_id
JOIN marketing_flow_entries e ON e.tenant_id=p.tenant_id AND e.id=p.entry_id
JOIN marketing_contacts c ON c.tenant_id=p.tenant_id AND c.list_id=e.list_id AND c.email=p.email
WHERE p.tenant_id=?1 AND p.flow_id=?2 AND p.email=?3 AND NOT p.unsubscribed AND p.stopped_at IS NULL AND f.status='active' AND c.opted_in)
AND NOT EXISTS(SELECT 1 FROM marketing_suppressions sup WHERE sup.tenant_id=?1 AND sup.email=?3)
ELSE (EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=?1 AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready'))) AND EXISTS(SELECT 1 FROM marketing_flow_positions fp WHERE fp.tenant_id=?1 AND fp.flow_id=?2 AND fp.email=?3 AND NOT fp.unsubscribed) AND NOT EXISTS(SELECT 1 FROM marketing_suppressions ms WHERE ms.tenant_id=?1 AND ms.email=?3)) END AS allowed;

-- name: PlatformCommitMarketingFlowFenced :one
-- scope: platform

SELECT urmail_commit_marketing_flow_fenced(?1,?2,?3,?4,?5,?6,?7,?8) AS result;

-- name: PlatformBeginMarketingFlowSend :one
-- scope: platform

SELECT urmail_begin_marketing_flow_send(?1,?2,?3,?4,?5,?6) AS result;

-- name: PlatformReleaseMarketingFlowReservation :one
-- scope: platform

SELECT urmail_release_marketing_flow_reservation(?1,?2,?3,?4,?5) AS released;

-- name: PlatformMarkMarketingFlowUncertainFenced :execrows
-- scope: platform

UPDATE marketing_flow_positions SET delivery_uncertain=true,last_error='delivery_uncertain',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND flow_id=?2 AND email=?3 AND lease_owner=?4 AND lease_fence=?5 AND lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: PlatformRecordMarketingFlowNodeExecution :execrows
-- scope: system

INSERT INTO marketing_flow_node_executions(tenant_id, position_id, lease_fence, node_key, status, error_code)
SELECT p.tenant_id, p.id, p.lease_fence, sqlc.arg(node_key), sqlc.arg(status), sqlc.arg(error_code)
FROM marketing_flow_positions p
WHERE p.tenant_id=sqlc.arg(tenant_id) AND p.flow_id=sqlc.arg(flow_id) AND p.email=sqlc.arg(email)
 AND p.lease_owner=sqlc.arg(lease_owner) AND p.lease_fence=sqlc.arg(lease_fence)
 AND p.lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000)
 AND EXISTS(SELECT 1 FROM marketing_flow_versions v WHERE v.tenant_id=p.tenant_id AND v.runtime_flow_id=p.flow_id)
ON CONFLICT(tenant_id, position_id, lease_fence) DO NOTHING;

-- name: PlatformBumpMarketingFlowAttempt :execrows
-- scope: system

UPDATE marketing_flow_positions
SET attempt_count=attempt_count+1,
 last_error=sqlc.arg(error_code),
 next_attempt_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) + sqlc.arg(backoff_seconds)*1000000
WHERE tenant_id=sqlc.arg(tenant_id) AND flow_id=sqlc.arg(flow_id) AND email=sqlc.arg(email)
 AND lease_owner=sqlc.arg(lease_owner) AND lease_fence=sqlc.arg(lease_fence);
