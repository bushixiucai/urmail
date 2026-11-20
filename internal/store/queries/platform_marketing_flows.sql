-- name: PlatformInsertMarketingFlow :one
-- scope: platform

INSERT INTO marketing_flows(tenant_id,name,status) VALUES(?1,?2,'draft') RETURNING id;

-- name: PlatformInsertMarketingFlowStep :exec
-- scope: platform

INSERT INTO marketing_flow_steps(tenant_id,flow_id,step_key,kind,next_key,opened_key,unsubscribed_key,position)
VALUES(?1,?2,?3,?4,?5,?6,?7,?8);

-- name: PlatformListMarketingFlowSteps :many
-- scope: platform

SELECT step_key,kind,next_key,opened_key,unsubscribed_key,position,wait_seconds
FROM marketing_flow_steps WHERE tenant_id=?1 AND flow_id=?2 ORDER BY position,id;

-- name: PlatformAdvanceMarketingFlow :one
-- scope: platform

INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key,unsubscribed)
VALUES(?1,?2,?3,?4,?5)
ON CONFLICT(tenant_id,flow_id,email) DO UPDATE
SET step_key=CASE WHEN marketing_flow_positions.unsubscribed THEN marketing_flow_positions.step_key ELSE EXCLUDED.step_key END,
 unsubscribed=marketing_flow_positions.unsubscribed OR EXCLUDED.unsubscribed,
 updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
RETURNING step_key,unsubscribed;

-- PlatformMarkMarketingFlowUncertain 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- urmail_mark_marketing_flow_uncertain = UPDATE 后按 rows-affected 返回 missing/uncertain。

-- Flow tick worker: recover expired leases and stale uncertain deliveries.
-- An uncertain delivery stays frozen for a quiet period, then the position
-- resumes on its current step: a flow must reach its end, and the duplicate
-- window is bounded and accepted (at-least-once), unlike the campaign path
-- where uncertain is terminal.
-- name: PlatformRecoverMarketingFlowLeases :execrows
-- scope: platform

UPDATE marketing_flow_positions SET lease_owner=NULL, lease_until=NULL
WHERE lease_until IS NOT NULL AND lease_until<=(CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: PlatformRecoverMarketingFlowUncertain :execrows
-- scope: platform

UPDATE marketing_flow_positions SET delivery_uncertain=false
WHERE delivery_uncertain AND updated_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000)-600000000
AND NOT EXISTS(SELECT 1 FROM marketing_flow_versions v WHERE v.tenant_id=marketing_flow_positions.tenant_id AND v.runtime_flow_id=marketing_flow_positions.flow_id);

-- Claim a bounded batch of due positions with SKIP LOCKED. A position is due
-- when its owning flow is active, it is subscribed, not uncertain, unleased
-- or expired, and either its step sends or its recorded wait has elapsed (or
-- still needs arming).

-- name: PlatformAdvanceMarketingFlowDueTenant :execrows
-- scope: platform

UPDATE platform_marketing_dispatch SET last_turn=(SELECT COALESCE(MAX(last_turn), 0) + 1 FROM platform_marketing_dispatch)
WHERE EXISTS(
 SELECT 1 FROM marketing_flow_positions p
 JOIN marketing_flows f ON f.tenant_id=p.tenant_id AND f.id=p.flow_id
 JOIN marketing_flow_steps s ON s.tenant_id=p.tenant_id AND s.flow_id=p.flow_id AND s.step_key=p.step_key
 WHERE p.tenant_id=tenant_id AND f.status='active' AND NOT p.unsubscribed AND NOT p.delivery_uncertain
  AND p.stopped_at IS NULL AND p.next_attempt_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000)
  AND (p.lease_until IS NULL OR p.lease_until<=(CAST(strftime('%s','now') AS INTEGER)*1000000))
  AND (s.kind<>'wait' OR p.wait_until IS NULL OR p.wait_until<=(CAST(strftime('%s','now') AS INTEGER)*1000000))
  AND (s.kind='wait' OR s.next_key<>''))
RETURNING tenant_id;

-- name: PlatformClaimMarketingFlowPositions :many
-- scope: platform

UPDATE marketing_flow_positions SET lease_owner=sqlc.arg(lease_owner),
 lease_until=(CAST(strftime('%s','now') AS INTEGER)*1000000) + sqlc.arg(lease_seconds)*1000000,
 lease_fence=lease_fence+1
WHERE tenant_id=sqlc.arg(tenant_id)
 AND EXISTS(SELECT 1  WHERE EXISTS(SELECT 1 FROM marketing_flow_steps s WHERE s.tenant_id=tenant_id AND s.flow_id=flow_id AND s.step_key=step_key AND s.kind<>'wait' OR (s.kind='wait' AND wait_until<=(CAST(strftime('%s','now') AS INTEGER)*1000000))) AND NOT unsubscribed AND NOT delivery_uncertain AND stopped_at IS NULL AND next_attempt_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000) AND (lease_until IS NULL OR lease_until<=(CAST(strftime('%s','now') AS INTEGER)*1000000))
RETURNING tenant_id, flow_id, email, step_key, lease_fence, lease_until, event_facts AND EXISTS(SELECT 1 FROM marketing_flow_versions v WHERE v.tenant_id=tenant_id AND v.runtime_flow_id=flow_id));
-- name: PlatformReleaseMarketingFlowLease :execrows
-- scope: platform

UPDATE marketing_flow_positions SET lease_owner=NULL, lease_until=NULL
WHERE tenant_id=?1 AND flow_id=?2 AND email=?3 AND lease_owner=?4 AND lease_fence=?5;

-- Arm a wait step: record wait_until from the step duration, only while the
-- claiming worker still owns the lease.
-- name: PlatformArmMarketingFlowWait :execrows
-- scope: platform

UPDATE marketing_flow_positions SET wait_until=(CAST(strftime('%s','now') AS INTEGER)*1000000)+(
  SELECT s.wait_seconds*1000000 FROM marketing_flow_steps s
  WHERE s.tenant_id=marketing_flow_positions.tenant_id AND s.flow_id=marketing_flow_positions.flow_id AND s.step_key=marketing_flow_positions.step_key
   AND s.kind='wait' AND s.wait_seconds>0)
WHERE tenant_id=?1 AND flow_id=?2 AND email=?3 AND wait_until IS NULL AND lease_owner=?4 AND lease_fence=?5 AND lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000);

-- Advance a wait step whose wait_until elapsed, without sending anything.
-- The database function re-checks uncertain, unsubscribe, the wait and the
-- sending gate under FOR UPDATE, so it is the linearization point.
-- name: PlatformCommitMarketingFlowWait :one
-- scope: platform

SELECT urmail_commit_marketing_flow_step(?1,?2,?3,sqlc.arg(event),sqlc.arg(next_key)) AS result;

-- The durable marketingflow.Store lives on the same tables.
-- name: PlatformGetMarketingFlowPosition :one
-- scope: platform

SELECT step_key, unsubscribed FROM marketing_flow_positions
WHERE tenant_id=?1 AND flow_id=?2 AND email=?3;

-- name: PlatformMarketingFlowMayAdvance :one
-- scope: platform

SELECT (EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=?1 AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready'))) AND EXISTS(SELECT 1 FROM marketing_flow_positions fp WHERE fp.tenant_id=?1 AND fp.flow_id=?2 AND fp.email=?3 AND NOT fp.unsubscribed) AND NOT EXISTS(SELECT 1 FROM marketing_suppressions ms WHERE ms.tenant_id=?1 AND ms.email=?3)) AS allowed;

-- name: PlatformSaveMarketingFlowPosition :exec
-- scope: platform

UPDATE marketing_flow_positions SET step_key=?4, unsubscribed=?5, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND flow_id=?2 AND email=?3;

-- Execution-time re-verification used by Store.StillAllowed: the sendable
-- check returns the step only when unsubscribe, wait, uncertainty and the
-- sending gate all pass.
-- name: PlatformMarketingFlowStillAllowed :one
-- scope: platform

SELECT (CASE
 WHEN NOT EXISTS(SELECT 1 FROM marketing_flow_positions p WHERE p.tenant_id=?1 AND p.flow_id=?2 AND p.email=?3) THEN 'missing'
 WHEN EXISTS(SELECT 1 FROM marketing_flow_positions p WHERE p.tenant_id=?1 AND p.flow_id=?2 AND p.email=?3 AND p.delivery_uncertain) THEN 'uncertain'
 WHEN EXISTS(SELECT 1 FROM marketing_flow_positions p WHERE p.tenant_id=?1 AND p.flow_id=?2 AND p.email=?3 AND (p.unsubscribed OR p.wait_until>(CAST(strftime('%s','now') AS INTEGER)*1000000))) THEN 'blocked'
 WHEN NOT EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=?1 AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready'))) THEN 'blocked'
 WHEN NOT EXISTS(SELECT 1 FROM marketing_flow_positions p WHERE p.tenant_id=?1 AND p.flow_id=?2 AND p.email=?3 AND NOT p.unsubscribed) THEN 'blocked'
 WHEN EXISTS(SELECT 1 FROM marketing_suppressions s WHERE s.tenant_id=?1 AND s.email=?3) THEN 'blocked'
 ELSE (SELECT p.step_key FROM marketing_flow_positions p WHERE p.tenant_id=?1 AND p.flow_id=?2 AND p.email=?3)
 END) NOT IN ('missing','blocked','uncertain') AS allowed;

-- Flow step content resolves from the tenant template named by the step key.
-- name: PlatformGetMarketingFlowTemplate :one
-- scope: platform

SELECT subject, text_body FROM marketing_templates WHERE tenant_id=?1 AND name=?2;

-- Execution-time re-verification for the flow send step: entitlement,
-- flow-active, unsubscribe and suppression are checked in one statement
-- immediately before SMTP. No row is written here.
-- name: PlatformAuthorizeMarketingFlowSend :one
-- scope: platform

SELECT (EXISTS(SELECT 1 FROM tenants mt WHERE mt.id=sqlc.arg(tenant_id) AND mt.status='active' AND (NOT EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id) OR EXISTS(SELECT 1 FROM domains gd WHERE gd.tenant_id=mt.id AND gd.status='active' AND gd.provisioning_state='ready')))
 AND EXISTS(SELECT 1 FROM marketing_flows f WHERE f.tenant_id=sqlc.arg(tenant_id) AND f.id=sqlc.arg(flow_id) AND f.status='active')
 AND EXISTS(SELECT 1 FROM marketing_flow_positions p WHERE p.tenant_id=sqlc.arg(tenant_id) AND p.flow_id=sqlc.arg(flow_id) AND p.email=sqlc.arg(email) AND NOT p.unsubscribed)
 AND NOT EXISTS(SELECT 1 FROM marketing_suppressions s WHERE s.tenant_id=sqlc.arg(tenant_id) AND s.email=sqlc.arg(email))
 AND EXISTS(SELECT 1 FROM marketing_channels ch WHERE ch.tenant_id=sqlc.arg(tenant_id) AND ch.id=sqlc.arg(channel_id) AND ch.enabled AND ch.kind='dedicated_gateway')) AS allowed;

-- name: PlatformCheckMarketingEmailSuppressed :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM marketing_suppressions WHERE tenant_id=?1 AND email=?2) AS suppressed;

-- An unsubscribe recorded between claim and send is terminal for the whole
-- flow position: current and all subsequent steps stop.
-- name: PlatformMarkMarketingFlowUnsubscribed :execrows
-- scope: platform

UPDATE marketing_flow_positions SET unsubscribed=true,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND flow_id=?2 AND email=?3 AND NOT unsubscribed;
