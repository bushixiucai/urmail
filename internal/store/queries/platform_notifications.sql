-- name: PlatformEnqueueNotification :exec
-- scope: platform

INSERT INTO notification_outbox(tenant_id,user_id,kind,message_key,params,dedupe_key,channel,state,recipient,recipient_verified)
VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10) ON CONFLICT(dedupe_key) DO NOTHING;

-- name: PlatformListOperationalAlerts :many
-- scope: platform

SELECT * FROM operational_alerts ORDER BY state,first_seen_at LIMIT 100;

-- name: PlatformGetNotificationChannel :one
-- scope: platform

SELECT value FROM settings WHERE scope='platform' AND scope_id=0 AND key='notification_channel';

-- name: PlatformClaimEmailNotification :one
-- scope: platform

-- OPS-103: claims one due email notification with a SKIP LOCKED lease. The
-- row stays 'pending' while leased, so a crashed worker's lease expiry makes
-- it reclaimable (at-least-once delivery); only a confirmed-ambiguous SMTP
-- submission later moves it to the unreplayable 'uncertain' state.
UPDATE notification_outbox n SET lease_until=(CAST(strftime('%s','now') AS INTEGER)*1000000),fence=fence+1,attempts=attempts+1
WHERE n.id=(SELECT id FROM notification_outbox WHERE channel='email' AND state='pending' AND next_attempt_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000)
 AND (lease_until IS NULL OR lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000)) AND attempts<8
 ORDER BY id LIMIT 1)
 AND n.channel='email' AND n.state='pending'
 AND (n.lease_until IS NULL OR n.lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000))
 RETURNING n.*;

-- name: PlatformFinishEmailNotification :execrows
-- scope: platform

-- Retries use exponential backoff of 1<<max(attempts-1,0) minutes capped at
-- 64 minutes; once attempts reach 8 a failing message is poisoned into the
-- 'failed' state and surfaced through telemetry instead of retrying forever.
UPDATE notification_outbox SET
 state=CASE WHEN ?3='pending' AND attempts>=8 THEN 'failed' ELSE ?3 END,
 delivered_at=CASE WHEN ?3='delivered' THEN (CAST(strftime('%s','now') AS INTEGER)*1000000) ELSE NULL END,
 lease_until=NULL,
 last_error=CASE WHEN ?3='delivered' THEN NULL ELSE NULLIF(?4,'') END,
 next_attempt_at=CASE WHEN ?3='pending' THEN (CAST(strftime('%s','now') AS INTEGER)*1000000)+make_interval(secs=>(60*(1<<min(max(attempts-1,0),6)))) ELSE next_attempt_at END
WHERE id=?1 AND fence=?2 AND state='pending';

-- name: PlatformQuarantinePoisonEmailNotifications :execrows
-- scope: platform

-- A worker that died repeatedly before finishing leaves an at-cap pending
-- row; its expired lease makes the poison sweep the one place that finalizes it.
UPDATE notification_outbox SET state='failed',last_error='notification delivery attempts exhausted',lease_until=NULL
WHERE channel='email' AND state='pending' AND attempts>=8 AND (lease_until IS NULL OR lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000));

-- name: PlatformMaxNotificationID :one
-- scope: platform

SELECT COALESCE(max(id),0) AS max_id FROM notification_outbox;

-- name: PlatformCountNewNotifications :many
-- scope: platform

SELECT channel,kind,count(*) AS count FROM notification_outbox
WHERE id>sqlc.arg(after_id) GROUP BY channel,kind;

-- name: PlatformNotificationOutboxCounts :many
-- scope: platform

SELECT channel,kind,state,count(*) AS count FROM notification_outbox GROUP BY channel,kind,state;

-- name: PlatformOperationalStateCounts :many
-- scope: platform

SELECT 'ai' AS kind,status,count(*) AS count FROM ai_usage_requests GROUP BY status
UNION ALL SELECT 'notification',state,count(*) FROM notification_outbox GROUP BY state
UNION ALL SELECT 'marketing_recipient',status,count(*) FROM marketing_recipients GROUP BY status
UNION ALL SELECT 'dns',status,count(*) FROM dns_change_plans GROUP BY status
UNION ALL SELECT 'onboarding',status,count(*) FROM onboarding_tasks GROUP BY status;

-- scope: platform

-- name: PlatformUpsertAIAlerts :execrows
-- scope: platform

WITH sources AS (
 SELECT 'ai-uncertain:'||id AS key, tenant_id, 'ai_requires_review' AS kind FROM ai_usage_requests
  WHERE status='uncertain' OR (status='reserved' AND updated_at<(CAST(strftime('%s','now') AS INTEGER)*1000000)-300000000)
)
INSERT INTO operational_alerts(alert_key, tenant_id, kind, state)
SELECT key, tenant_id, kind, 'firing' FROM sources
ON CONFLICT(alert_key) DO UPDATE SET state='firing',
 last_seen_at=(CAST(strftime('%s','now') AS INTEGER)*1000000), resolved_at=NULL;

-- name: PlatformResolveStaleAIAlerts :execrows
-- scope: platform

UPDATE operational_alerts a SET state='resolved',
 resolved_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),
 last_seen_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE a.kind IN('ai_requires_review','ai_budget_exhausted') AND a.state='firing'
 AND NOT EXISTS(
  SELECT 1 FROM ai_usage_requests u WHERE u.status='uncertain' OR (u.status='reserved' AND u.updated_at<(CAST(strftime('%s','now') AS INTEGER)*1000000)-300000000));
-- name: PlatformEnqueueAIAlerts :exec
-- scope: platform

INSERT INTO notification_outbox(tenant_id,kind,message_key,params,dedupe_key,channel,state)
SELECT a.tenant_id,a.kind,'notification.'||a.kind,'{}',a.alert_key||':'||to_char((CAST(strftime('%s','now') AS INTEGER)*1000000),'YYYY-MM-DD-HH24'),'in_app','delivered'
FROM operational_alerts a WHERE a.kind IN('ai_requires_review','ai_budget_exhausted') AND a.state='firing'
ON CONFLICT(dedupe_key) DO NOTHING;

-- name: PlatformGetSubscriptionNoticeDays :one
-- scope: platform

SELECT value FROM settings WHERE scope='platform' AND scope_id=0 AND key='subscription_notice_days';
