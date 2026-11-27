-- name: PlatformMarketingFlowIsVersioned :one
SELECT EXISTS(SELECT 1 FROM marketing_flow_versions v WHERE v.tenant_id=$1 AND v.runtime_flow_id=$2)::boolean AS versioned;

-- name: PlatformGetMarketingFlowSnapshotContent :one
SELECT subject,text_body FROM marketing_flow_content_snapshots WHERE tenant_id=$1 AND runtime_flow_id=$2 AND step_key=$3;

-- name: PlatformCheckMarketingFlowLease :one
SELECT EXISTS(SELECT 1 FROM marketing_flow_positions p JOIN marketing_flows f ON f.tenant_id=p.tenant_id AND f.id=p.flow_id
WHERE p.tenant_id=$1 AND p.flow_id=$2 AND p.email=$3 AND p.lease_owner=$4 AND p.lease_fence=$5 AND p.lease_until>now() AND p.stopped_at IS NULL AND f.status='active')::boolean AS allowed;

-- name: PlatformVersionedMarketingFlowMayAdvance :one
SELECT CASE WHEN EXISTS(SELECT 1 FROM marketing_flow_versions v WHERE v.tenant_id=$1 AND v.runtime_flow_id=$2)
THEN urmail_mail_allowed($1)
AND EXISTS(SELECT 1 FROM marketing_flow_positions p JOIN marketing_flows f ON f.tenant_id=p.tenant_id AND f.id=p.flow_id
JOIN marketing_flow_entries e ON e.tenant_id=p.tenant_id AND e.id=p.entry_id
JOIN marketing_contacts c ON c.tenant_id=p.tenant_id AND c.list_id=e.list_id AND c.email=p.email
WHERE p.tenant_id=$1 AND p.flow_id=$2 AND p.email=$3 AND NOT p.unsubscribed AND p.stopped_at IS NULL AND f.status='active' AND c.opted_in)
AND NOT EXISTS(SELECT 1 FROM marketing_suppressions sup WHERE sup.tenant_id=$1 AND sup.email=$3)
ELSE urmail_marketing_flow_may_advance($1,$2,$3) END::boolean AS allowed;

-- name: PlatformCommitMarketingFlowFenced :one
SELECT urmail_commit_marketing_flow_fenced($1,$2,$3,$4,$5,$6,$7,$8)::text AS result;

-- name: PlatformBeginMarketingFlowSend :one
SELECT urmail_begin_marketing_flow_send($1,$2,$3,$4,$5,$6)::text AS result;

-- name: PlatformReleaseMarketingFlowReservation :one
SELECT urmail_release_marketing_flow_reservation($1,$2,$3,$4,$5)::boolean AS released;

-- name: PlatformMarkMarketingFlowUncertainFenced :execrows
UPDATE marketing_flow_positions SET delivery_uncertain=true,last_error='delivery_uncertain',updated_at=now()
WHERE tenant_id=$1 AND flow_id=$2 AND email=$3 AND lease_owner=$4 AND lease_fence=$5 AND lease_until>now();

-- name: PlatformRecordMarketingFlowExecution :execrows
WITH recorded AS (
INSERT INTO marketing_flow_node_executions(tenant_id,position_id,lease_fence,node_key,status,error_code)
SELECT p.tenant_id,p.id,p.lease_fence,sqlc.arg(node_key)::text,sqlc.arg(status)::text,sqlc.arg(error_code)::text
FROM marketing_flow_positions p WHERE p.tenant_id=sqlc.arg(tenant_id)::bigint AND p.flow_id=sqlc.arg(flow_id)::bigint AND p.email=sqlc.arg(email)::text
AND p.lease_owner=sqlc.arg(lease_owner)::text AND p.lease_fence=sqlc.arg(lease_fence)::bigint AND p.lease_until>now()
AND EXISTS(SELECT 1 FROM marketing_flow_versions v WHERE v.tenant_id=p.tenant_id AND v.runtime_flow_id=p.flow_id)
ON CONFLICT(tenant_id,position_id,lease_fence) DO NOTHING RETURNING position_id
)
UPDATE marketing_flow_positions SET attempt_count=attempt_count+1,last_error=sqlc.arg(error_code)::text,
next_attempt_at=now()+make_interval(secs=>sqlc.arg(retry_seconds)::int),updated_at=now()
WHERE tenant_id=sqlc.arg(tenant_id)::bigint AND id IN(SELECT position_id FROM recorded);
