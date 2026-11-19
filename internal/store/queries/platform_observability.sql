-- name: PlatformSetOperationalCondition :exec
-- scope: platform

INSERT INTO operational_alerts(alert_key,kind,state,resolved_at)
SELECT @alert_key,@kind,CASE WHEN @firing THEN 'firing' ELSE 'resolved' END,CASE WHEN @firing THEN NULL ELSE (CAST(strftime('%s','now') AS INTEGER)*1000000) END
WHERE @firing OR EXISTS(SELECT 1 FROM operational_alerts WHERE alert_key= @alert_key)
ON CONFLICT(alert_key) DO UPDATE SET
 state=EXCLUDED.state,last_seen_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),
 first_seen_at=CASE WHEN operational_alerts.state='resolved' AND EXCLUDED.state='firing' THEN (CAST(strftime('%s','now') AS INTEGER)*1000000) ELSE operational_alerts.first_seen_at END,
 resolved_at=CASE WHEN EXCLUDED.state='firing' THEN NULL WHEN operational_alerts.state='firing' THEN (CAST(strftime('%s','now') AS INTEGER)*1000000) ELSE operational_alerts.resolved_at END;

-- name: PlatformEnqueueObservabilityAlerts :exec
-- scope: platform

INSERT INTO notification_outbox(kind,message_key,params,dedupe_key,channel,state)
SELECT 'alert_'||CASE WHEN state='firing' THEN 'firing' ELSE 'resolved' END,CASE WHEN state='firing' THEN 'notification.operational_firing' ELSE 'notification.operational_resolved' END,
 json_object('kind',kind,'source',alert_key,'state',state),
 'operational:'||alert_key||':'||state||':'||CASE WHEN state='firing' THEN to_char((CAST(strftime('%s','now') AS INTEGER)*1000000),'YYYY-MM-DD-HH24') ELSE ((resolved_at)/1000000) END,
 'in_app','delivered'
FROM operational_alerts WHERE kind LIKE 'telemetry_%' OR kind LIKE 'archive_%'
ON CONFLICT(dedupe_key) DO NOTHING;

-- OPS-003: single-row backup freshness facts for the alert evaluator. The
-- latest run's state detects a failed pipeline; the newest successful
-- finished_at detects a stale (missed) backup schedule.
-- name: PlatformBackupFreshness :one
-- scope: platform

SELECT
 coalesce((SELECT state FROM backup_runs ORDER BY started_at DESC,id DESC LIMIT 1),'') AS latest_state,
 (SELECT max(finished_at) FROM backup_runs WHERE state='success') AS last_success_at;

-- OPS-003: read only the operational threshold setting so the alert evaluator
-- never depends on unrelated platform settings keys.
-- name: PlatformGetObservabilityThresholds :many
-- scope: platform

SELECT key,value FROM settings WHERE scope='platform' AND scope_id=0 AND key='observability_thresholds';

-- name: PlatformArchivePathIndexed :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM archive_entries WHERE path= @path);
