-- name: PlatformSetOperationalCondition :exec
INSERT INTO operational_alerts(alert_key,kind,state,resolved_at)
SELECT @alert_key,@kind,CASE WHEN @firing::boolean THEN 'firing' ELSE 'resolved' END,CASE WHEN @firing::boolean THEN NULL ELSE statement_timestamp() END
WHERE @firing::boolean OR EXISTS(SELECT 1 FROM operational_alerts WHERE alert_key= @alert_key)
ON CONFLICT(alert_key) DO UPDATE SET
 state=EXCLUDED.state,last_seen_at=statement_timestamp(),
 first_seen_at=CASE WHEN operational_alerts.state='resolved' AND EXCLUDED.state='firing' THEN statement_timestamp() ELSE operational_alerts.first_seen_at END,
 resolved_at=CASE WHEN EXCLUDED.state='firing' THEN NULL WHEN operational_alerts.state='firing' THEN statement_timestamp() ELSE operational_alerts.resolved_at END;

-- name: PlatformEnqueueObservabilityAlerts :exec
INSERT INTO notification_outbox(kind,message_key,params,dedupe_key,channel,state)
SELECT kind,CASE WHEN state='firing' THEN 'notification.operational_firing' ELSE 'notification.operational_resolved' END,
 jsonb_build_object('kind',kind,'source',alert_key,'state',state),
 'operational:'||alert_key||':'||state||':'||CASE WHEN state='firing' THEN to_char(statement_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD-HH24') ELSE extract(epoch FROM resolved_at)::text END,
 'in_app','delivered'
FROM operational_alerts WHERE kind LIKE 'telemetry_%' OR kind LIKE 'archive_%'
ON CONFLICT(dedupe_key) DO NOTHING;

-- name: PlatformArchivePathIndexed :one
SELECT EXISTS(SELECT 1 FROM archive_entries WHERE path= @path::text)::boolean;
