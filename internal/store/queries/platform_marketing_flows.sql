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

-- name: PlatformMarkMarketingFlowUncertain :one
-- scope: platform

SELECT urmail_mark_marketing_flow_uncertain(?1,?2,?3);

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
-- name: PlatformClaimMarketingFlowPositions :many
-- scope: platform

WITH due_tenants AS MATERIALIZED (
 SELECT d.tenant_id,d.last_turn FROM platform_marketing_dispatch d
 WHERE EXISTS(SELECT 1 FROM marketing_flow_positions p
 JOIN marketing_flows f ON f.tenant_id=p.tenant_id AND f.id=p.flow_id
 JOIN marketing_flow_steps s ON s.tenant_id=p.tenant_id AND s.flow_id=p.flow_id AND s.step_key=p.step_key
 WHERE p.tenant_id=d.tenant_id AND f.status='active' AND NOT p.unsubscribed AND NOT p.delivery_uncertain AND p.stopped_at IS NULL AND p.next_attempt_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000)
 AND (p.lease_until IS NULL OR p.lease_until<=(CAST(strftime('%s','now') AS INTEGER)*1000000)) AND (s.kind<>'wait' OR p.wait_until IS NULL OR p.wait_until<=(CAST(strftime('%s','now') AS INTEGER)*1000000)) AND (s.kind='wait' OR s.next_key<>''))
 ORDER BY d.last_turn,d.tenant_id LIMIT sqlc.arg(batch)
), heads AS MATERIALIZED (
 SELECT p.*,d.last_turn,row_number() OVER(PARTITION BY p.tenant_id ORDER BY p.id) AS tenant_order
 FROM due_tenants d CROSS JOIN LATERAL (
 SELECT p.id,p.tenant_id,s.kind AS step_kind,s.next_key AS step_next,s.wait_seconds AS step_wait_seconds,
 s.opened_key AS step_otherwise,s.condition_field,s.condition_operator,s.condition_value
 FROM marketing_flow_positions p
 JOIN marketing_flows f ON f.tenant_id=p.tenant_id AND f.id=p.flow_id
 JOIN marketing_flow_steps s ON s.tenant_id=p.tenant_id AND s.flow_id=p.flow_id AND s.step_key=p.step_key
 WHERE p.tenant_id=d.tenant_id AND f.status='active' AND NOT p.unsubscribed AND NOT p.delivery_uncertain AND p.stopped_at IS NULL AND p.next_attempt_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000)
 AND (p.lease_until IS NULL OR p.lease_until<=(CAST(strftime('%s','now') AS INTEGER)*1000000)) AND (s.kind<>'wait' OR p.wait_until IS NULL OR p.wait_until<=(CAST(strftime('%s','now') AS INTEGER)*1000000)) AND (s.kind='wait' OR s.next_key<>'')
 ORDER BY p.id LIMIT sqlc.arg(batch)
 ) p
), candidate AS MATERIALIZED (
 SELECT p.id,h.tenant_id,h.step_kind,h.step_next,h.step_wait_seconds,h.step_otherwise,h.condition_field,h.condition_operator,h.condition_value
 FROM marketing_flow_positions p JOIN heads h ON h.id=p.id AND h.tenant_id=p.tenant_id
 ORDER BY h.tenant_order,h.last_turn,h.tenant_id,p.id LIMIT sqlc.arg(batch)
), turn AS (
 UPDATE platform_marketing_dispatch d SET last_turn=nextval('platform_marketing_dispatch_turn_seq')
 WHERE d.tenant_id IN(SELECT tenant_id FROM candidate) RETURNING d.tenant_id
)
UPDATE marketing_flow_positions p
SET lease_owner=sqlc.arg(lease_owner),
 lease_until=(CAST(strftime('%s','now') AS INTEGER)*1000000)+make_interval(secs=>sqlc.arg(lease_seconds)),
 lease_fence=p.lease_fence+1
FROM candidate JOIN turn ON turn.tenant_id=candidate.tenant_id
WHERE p.id=candidate.id
RETURNING p.tenant_id, p.flow_id, p.email, p.step_key, p.lease_fence,p.lease_until,p.event_facts,
 candidate.step_kind, candidate.step_next, candidate.step_wait_seconds,candidate.step_otherwise,candidate.condition_field,candidate.condition_operator,candidate.condition_value;

-- Release the claim only when this worker still owns the lease and fence.
-- name: PlatformReleaseMarketingFlowLease :execrows
-- scope: platform

UPDATE marketing_flow_positions SET lease_owner=NULL, lease_until=NULL
WHERE tenant_id=?1 AND flow_id=?2 AND email=?3 AND lease_owner=?4 AND lease_fence=?5;

-- Arm a wait step: record wait_until from the step duration, only while the
-- claiming worker still owns the lease.
-- name: PlatformArmMarketingFlowWait :execrows
-- scope: platform

UPDATE marketing_flow_positions p SET wait_until=(CAST(strftime('%s','now') AS INTEGER)*1000000)+make_interval(secs=>s.wait_seconds)
FROM marketing_flow_steps s
WHERE s.tenant_id=p.tenant_id AND s.flow_id=p.flow_id AND s.step_key=p.step_key
 AND s.kind='wait' AND s.wait_seconds>0
 AND p.tenant_id=?1 AND p.flow_id=?2 AND p.email=?3
 AND p.wait_until IS NULL AND p.lease_owner=?4 AND p.lease_fence=?5 AND p.lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000);

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

SELECT urmail_marketing_flow_may_advance(?1,?2,?3) AS allowed;

-- name: PlatformSaveMarketingFlowPosition :exec
-- scope: platform

UPDATE marketing_flow_positions SET step_key=?4, unsubscribed=?5, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND flow_id=?2 AND email=?3;

-- Execution-time re-verification used by Store.StillAllowed: the sendable
-- check returns the step only when unsubscribe, wait, uncertainty and the
-- sending gate all pass.
-- name: PlatformMarketingFlowStillAllowed :one
-- scope: platform

SELECT (urmail_marketing_flow_sendable(?1,?2,?3) NOT IN ('missing','blocked','uncertain')) AS allowed;

-- Flow step content resolves from the tenant template named by the step key.
-- name: PlatformGetMarketingFlowTemplate :one
-- scope: platform

SELECT subject, text_body FROM marketing_templates WHERE tenant_id=?1 AND name=?2;

-- Execution-time re-verification for the flow send step: entitlement,
-- flow-active, unsubscribe and suppression are checked in one statement
-- immediately before SMTP. No row is written here.
-- name: PlatformAuthorizeMarketingFlowSend :one
-- scope: platform

SELECT (urmail_mail_allowed(sqlc.arg(tenant_id))
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
