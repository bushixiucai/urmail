-- name: PlatformEnqueueNotification :exec
INSERT INTO notification_outbox(tenant_id,user_id,kind,message_key,params,dedupe_key,channel,state,recipient,recipient_verified)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(dedupe_key) DO NOTHING;

-- name: PlatformListOperationalAlerts :many
SELECT * FROM operational_alerts ORDER BY state,first_seen_at LIMIT 100;

-- name: PlatformRefreshPaymentAlerts :exec
INSERT INTO operational_alerts(alert_key,tenant_id,kind,state)
SELECT 'payment-review:'||id,tenant_id,'payment_manual_review','firing' FROM payment_manual_reviews WHERE status='open'
ON CONFLICT(alert_key) DO UPDATE SET state='firing',last_seen_at=now(),resolved_at=NULL;

-- name: PlatformResolvePaymentAlerts :exec
UPDATE operational_alerts a SET state='resolved',resolved_at=now(),last_seen_at=now()
WHERE a.kind='payment_manual_review' AND a.state='firing'
AND NOT EXISTS(SELECT 1 FROM payment_manual_reviews r WHERE a.alert_key='payment-review:'||r.id AND r.status='open');

-- name: PlatformEnqueuePaymentAlerts :exec
INSERT INTO notification_outbox(kind,message_key,params,dedupe_key,channel,state)
SELECT 'payment_manual_review','notification.payment_manual_review',jsonb_build_object('review_id',r.id,'order_id',r.order_id),'payment-review:'||r.id||':'||to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD-HH24'),'in_app','delivered'
FROM payment_manual_reviews r WHERE r.status='open' ON CONFLICT(dedupe_key) DO NOTHING;

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

-- name: PlatformEnqueueSubscriptionLifecycleNotices :exec
-- OPS-103 lifecycle gap closure: grace-period start (past_due), suspension
-- after the grace period ends (tenant suspended) and restoration after a late
-- settlement (a renewal attempt that succeeded only once its paid period had
-- already lapsed). Dedupe keys make every episode idempotent; in-app rows are
-- delivered atomically with the insert, email rows enter the retry queue.
WITH sources AS (
 SELECT s.tenant_id,s.id AS subscription_id,s.plan_id,s.period_end,'grace'::text AS event
 FROM subscriptions s JOIN tenants t ON t.id=s.tenant_id
 WHERE s.status='past_due' AND t.status IN('active','pending')
 UNION ALL
 SELECT s.tenant_id,s.id,s.plan_id,s.period_end,'restored'
 FROM subscriptions s
 JOIN subscription_renewal_attempts a ON a.subscription_id=s.id AND a.tenant_id=s.tenant_id
  AND a.state='succeeded' AND a.period_end<=a.updated_at
 WHERE s.status='active'
 UNION ALL
 SELECT t.id,NULL::bigint,s.plan_id,NULL::timestamptz,'suspended'
 FROM tenants t JOIN subscriptions s ON s.tenant_id=t.id
 WHERE t.status='suspended' AND s.status IN('past_due','active')
)
INSERT INTO notification_outbox(tenant_id,user_id,kind,message_key,params,dedupe_key,channel,state,recipient,recipient_verified)
SELECT src.tenant_id,t.owner_user_id,'subscription_'||src.event,'notification.subscription_'||src.event,
 jsonb_build_object('plan',src.plan_id,'period_end',src.period_end),
 CASE src.event
  WHEN 'suspended' THEN 'lifecycle:suspended:'||src.tenant_id
  WHEN 'restored' THEN 'lifecycle:restored:'||src.subscription_id||':'||extract(epoch FROM src.period_end)::text
  ELSE 'lifecycle:grace:'||src.subscription_id||':'||extract(epoch FROM src.period_end)::text END,
 ch.channel,
 CASE WHEN ch.channel='in_app' THEN 'delivered' ELSE 'pending' END,
 CASE WHEN ch.channel='email' THEN tc.email END,ch.channel='email'
FROM sources src
JOIN tenants t ON t.id=src.tenant_id
LEFT JOIN tenant_contacts tc ON tc.tenant_id=src.tenant_id
CROSS JOIN (VALUES('in_app'),('email')) ch(channel)
WHERE (ch.channel='in_app' AND t.owner_user_id IS NOT NULL) OR (ch.channel='email' AND tc.email IS NOT NULL)
ON CONFLICT(dedupe_key) DO NOTHING;

-- name: PlatformCountOpenPaymentReviews :one
SELECT count(*) FROM payment_manual_reviews WHERE status='open';

-- name: PlatformEnqueueSubscriptionReminders :exec
WITH configured_days AS (
 SELECT DISTINCT CASE WHEN raw~'^[0-9]{1,3}$' THEN raw::int END AS days
 FROM jsonb_array_elements_text(COALESCE((SELECT CASE WHEN jsonb_typeof(value)='array' THEN value ELSE '[]'::jsonb END FROM settings WHERE scope='platform' AND scope_id=0 AND key='subscription_notice_days'),'[7,3,1,0]'::jsonb)) raw
), reminders AS (
 SELECT s.tenant_id,t.owner_user_id,tc.email,s.period_end,s.plan_id,d.days
 FROM subscriptions s JOIN tenants t ON t.id=s.tenant_id
 LEFT JOIN tenant_contacts tc ON tc.tenant_id=s.tenant_id
 CROSS JOIN configured_days d
 WHERE s.status IN('active','trialing','past_due') AND t.status IN('active','pending') AND d.days IS NOT NULL
 AND (s.period_end AT TIME ZONE urmail_business_timezone(s.tenant_id))::date
     =(now() AT TIME ZONE urmail_business_timezone(s.tenant_id))::date+d.days
)
INSERT INTO notification_outbox(tenant_id,user_id,kind,message_key,params,dedupe_key,channel,state,recipient,recipient_verified)
SELECT r.tenant_id,r.owner_user_id,'subscription_expiry','notification.subscription_expiry',jsonb_build_object('days',r.days,'plan',r.plan_id,'period_end',r.period_end),
 'subscription-expiry:'||r.tenant_id||':'||extract(epoch FROM r.period_end)::text||':'||r.days||':'||ch.channel,ch.channel,
 CASE WHEN ch.channel='in_app' THEN 'delivered' ELSE 'pending' END,
 CASE WHEN ch.channel='email' THEN r.email END,ch.channel='email'
FROM reminders r CROSS JOIN (VALUES('in_app'),('email'))ch(channel)
WHERE ch.channel='in_app' OR r.email IS NOT NULL
ON CONFLICT(dedupe_key) DO NOTHING;

-- name: PlatformRefreshRefundAlerts :exec
INSERT INTO operational_alerts(alert_key,tenant_id,kind,state)
SELECT 'refund:'||id,tenant_id,'refund_requires_review','firing' FROM refunds WHERE status='uncertain' OR (status='processing' AND request_started_at<now()-interval '2 minutes') OR (status='approved' AND last_error_code IS NOT NULL)
ON CONFLICT(alert_key) DO UPDATE SET state='firing',last_seen_at=now(),resolved_at=NULL;

-- name: PlatformResolveRefundAlerts :exec
UPDATE operational_alerts a SET state='resolved',resolved_at=now(),last_seen_at=now() WHERE a.kind='refund_requires_review' AND a.state='firing'
AND NOT EXISTS(SELECT 1 FROM refunds r WHERE a.alert_key='refund:'||r.id AND (r.status='uncertain' OR (r.status='processing' AND r.request_started_at<now()-interval '2 minutes') OR (r.status='approved' AND r.last_error_code IS NOT NULL)));

-- name: PlatformEnqueueRefundAlerts :exec
INSERT INTO notification_outbox(kind,message_key,params,dedupe_key,channel,state)
SELECT 'refund_requires_review','notification.refund_requires_review',jsonb_build_object('refund_id',r.id,'order_id',r.order_id),'refund-review:'||r.id||':'||to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD-HH24'),'in_app','delivered'
FROM refunds r WHERE r.status='uncertain' OR (r.status='processing' AND r.request_started_at<now()-interval '2 minutes') OR (r.status='approved' AND r.last_error_code IS NOT NULL)
ON CONFLICT(dedupe_key) DO NOTHING;

-- name: PlatformEnqueueRefundResult :exec
INSERT INTO notification_outbox(tenant_id,user_id,kind,message_key,params,dedupe_key,channel,state,recipient,recipient_verified)
SELECT r.tenant_id,t.owner_user_id,'refund_result','notification.refund_'||r.status,jsonb_build_object('refund_id',r.id,'order_id',r.order_id,'amount_cents',r.amount_cents,'currency',r.currency),
 'refund-result:'||r.id||':'||r.status||':'||ch.channel,ch.channel,CASE WHEN ch.channel='in_app' THEN 'delivered' ELSE 'pending' END,CASE WHEN ch.channel='email' THEN tc.email END,ch.channel='email'
FROM refunds r JOIN tenants t ON t.id=r.tenant_id LEFT JOIN tenant_contacts tc ON tc.tenant_id=r.tenant_id CROSS JOIN(VALUES('in_app'),('email'))ch(channel)
WHERE r.id=$1 AND r.status IN('completed','failed') AND ((ch.channel='in_app' AND t.owner_user_id IS NOT NULL) OR (ch.channel='email' AND tc.email IS NOT NULL))
ON CONFLICT(dedupe_key) DO NOTHING;

-- name: PlatformRefundStateCounts :many
SELECT status,count(*)::bigint AS count FROM refunds GROUP BY status;

-- name: PlatformAdvanceScheduledPlanChanges :exec
UPDATE subscription_changes c SET status=CASE WHEN urmail_plan_snapshot_fits(c.tenant_id,t.plan_snapshot) THEN 'completed' ELSE 'blocked' END
FROM subscription_terms t WHERE t.tenant_id=c.tenant_id AND t.period_start<=now() AND t.period_end>now() AND t.revoked_at IS NULL AND t.plan_snapshot->>'code'=c.target_plan_code
AND c.effective_at<=now() AND c.status IN('scheduled','blocked');

-- name: PlatformOperationalStateCounts :many
SELECT 'refund'::text AS kind,status,count(*)::bigint AS count FROM refunds GROUP BY status
UNION ALL SELECT 'checkout',state,count(*) FROM payment_checkout_intents GROUP BY state
UNION ALL SELECT 'ai',status,count(*) FROM ai_usage_requests GROUP BY status
UNION ALL SELECT 'notification',state,count(*) FROM notification_outbox GROUP BY state
UNION ALL SELECT 'marketing_recipient',status,count(*) FROM marketing_recipients GROUP BY status
UNION ALL SELECT 'dns',status,count(*) FROM dns_change_plans GROUP BY status
UNION ALL SELECT 'onboarding',status,count(*) FROM onboarding_tasks GROUP BY status;

-- name: PlatformRefreshAIAlerts :exec
WITH usage AS (
 SELECT tenant_id,period_start,period_end,count(*) AS calls,
 sum(CASE WHEN mode='platform' THEN COALESCE(actual_tokens,reserved_tokens) ELSE 0 END) AS tokens,
 sum(CASE WHEN mode='platform' THEN COALESCE(actual_cost_micros,reserved_cost_micros) ELSE 0 END) AS cost
 FROM ai_usage_requests WHERE status<>'released' AND period_start<=now() AND period_end>now() GROUP BY tenant_id,period_start,period_end
), sources AS (
 SELECT 'ai-uncertain:'||id AS key,tenant_id,'ai_requires_review'::text AS kind FROM ai_usage_requests WHERE status='uncertain' OR (status='reserved' AND updated_at<now()-interval '5 minutes')
 UNION ALL
 SELECT 'ai-budget:'||u.tenant_id||':'||extract(epoch FROM u.period_start),u.tenant_id,'ai_budget_exhausted'
 FROM usage u JOIN LATERAL urmail_effective_plan(u.tenant_id) p ON true
 WHERE (CASE WHEN p.features#>>'{ai,calls}' ~ '^[0-9]{1,16}$' THEN u.calls>=(p.features#>>'{ai,calls}')::numeric AND (p.features#>>'{ai,calls}')::numeric>0 ELSE false END)
 OR (CASE WHEN p.features#>>'{ai,tokens}' ~ '^[0-9]{1,16}$' THEN u.tokens>=(p.features#>>'{ai,tokens}')::numeric AND (p.features#>>'{ai,tokens}')::numeric>0 ELSE false END)
 OR (CASE WHEN p.features#>>'{ai,cost_micros}' ~ '^[0-9]{1,16}$' THEN u.cost>=(p.features#>>'{ai,cost_micros}')::numeric AND (p.features#>>'{ai,cost_micros}')::numeric>0 ELSE false END)
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
