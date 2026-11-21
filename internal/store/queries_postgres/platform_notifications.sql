-- name: PlatformEnqueueNotification :exec
INSERT INTO notification_outbox(tenant_id,user_id,kind,message_key,params,dedupe_key,channel,state,recipient,recipient_verified)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(dedupe_key) DO NOTHING;

-- name: PlatformListOperationalAlerts :many
SELECT * FROM operational_alerts ORDER BY state,first_seen_at LIMIT 100;

-- name: PlatformGetNotificationChannel :one
SELECT value FROM settings WHERE scope='platform' AND scope_id=0 AND key='notification_channel';

-- name: PlatformClaimEmailNotification :one
-- OPS-103: claims one due email notification with a SKIP LOCKED lease. The
-- row stays 'pending' while leased, so a crashed worker's lease expiry makes
-- it reclaimable (at-least-once delivery); only a confirmed-ambiguous SMTP
-- submission later moves it to the unreplayable 'uncertain' state.
UPDATE notification_outbox n SET lease_until=now()+interval '2 minutes',fence=fence+1,attempts=attempts+1
FROM (SELECT id FROM notification_outbox WHERE channel='email' AND state='pending' AND next_attempt_at<=now()
 AND (lease_until IS NULL OR lease_until<now()) AND attempts<8
 ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1) candidate
WHERE n.id=candidate.id RETURNING n.*;

-- name: PlatformFinishEmailNotification :execrows
-- Retries use exponential backoff of 1<<max(attempts-1,0) minutes capped at
-- 64 minutes; once attempts reach 8 a failing message is poisoned into the
-- 'failed' state and surfaced through telemetry instead of retrying forever.
UPDATE notification_outbox SET
 state=CASE WHEN $3::text='pending' AND attempts>=8 THEN 'failed' ELSE $3 END,
 delivered_at=CASE WHEN $3='delivered' THEN now() ELSE NULL END,
 lease_until=NULL,
 last_error=CASE WHEN $3='delivered' THEN NULL ELSE NULLIF($4::text,'') END,
 next_attempt_at=CASE WHEN $3='pending' THEN now()+make_interval(secs=>(60*(1<<LEAST(GREATEST(attempts-1,0),6)))::int) ELSE next_attempt_at END
WHERE id=$1 AND fence=$2 AND state='pending';

-- name: PlatformQuarantinePoisonEmailNotifications :execrows
-- A worker that died repeatedly before finishing leaves an at-cap pending
-- row; its expired lease makes the poison sweep the one place that finalizes it.
UPDATE notification_outbox SET state='failed',last_error='notification delivery attempts exhausted',lease_until=NULL
WHERE channel='email' AND state='pending' AND attempts>=8 AND (lease_until IS NULL OR lease_until<now());

-- name: PlatformMaxNotificationID :one
SELECT COALESCE(max(id),0)::bigint AS max_id FROM notification_outbox;

-- name: PlatformCountNewNotifications :many
SELECT channel,kind,count(*)::bigint AS count FROM notification_outbox
WHERE id>sqlc.arg(after_id) GROUP BY channel,kind;

-- name: PlatformNotificationOutboxCounts :many
SELECT channel,kind,state,count(*)::bigint AS count FROM notification_outbox GROUP BY channel,kind,state;

-- name: PlatformOperationalStateCounts :many
SELECT 'ai'::text AS kind,status,count(*)::bigint AS count FROM ai_usage_requests GROUP BY status
UNION ALL SELECT 'notification',state,count(*) FROM notification_outbox GROUP BY state
UNION ALL SELECT 'marketing_recipient',status,count(*) FROM marketing_recipients GROUP BY status
UNION ALL SELECT 'dns',status,count(*) FROM dns_change_plans GROUP BY status
UNION ALL SELECT 'onboarding',status,count(*) FROM onboarding_tasks GROUP BY status;

-- name: PlatformRefreshAIAlerts :exec
WITH sources AS (
 SELECT 'ai-uncertain:'||id AS key,tenant_id,'ai_requires_review'::text AS kind FROM ai_usage_requests WHERE status='uncertain' OR (status='reserved' AND updated_at<now()-interval '5 minutes')
), firing AS (
 INSERT INTO operational_alerts(alert_key,tenant_id,kind,state) SELECT key,tenant_id,kind,'firing' FROM sources
 ON CONFLICT(alert_key) DO UPDATE SET state='firing',last_seen_at=now(),resolved_at=NULL RETURNING alert_key
)
UPDATE operational_alerts a SET state='resolved',resolved_at=now(),last_seen_at=now()
WHERE a.kind IN('ai_requires_review','ai_budget_exhausted') AND a.state='firing' AND NOT EXISTS(SELECT 1 FROM sources s WHERE s.key=a.alert_key);

-- name: PlatformEnqueueAIAlerts :exec
INSERT INTO notification_outbox(tenant_id,kind,message_key,params,dedupe_key,channel,state)
SELECT a.tenant_id,a.kind,'notification.'||a.kind,'{}'::jsonb,a.alert_key||':'||to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD-HH24'),'in_app','delivered'
FROM operational_alerts a WHERE a.kind IN('ai_requires_review','ai_budget_exhausted') AND a.state='firing'
ON CONFLICT(dedupe_key) DO NOTHING;

-- name: PlatformGetSubscriptionNoticeDays :one
SELECT value FROM settings WHERE scope='platform' AND scope_id=0 AND key='subscription_notice_days';
