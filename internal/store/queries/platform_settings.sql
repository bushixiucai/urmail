-- name: PlatformListSettings :many
-- scope: platform

SELECT key,value,updated_at FROM settings WHERE scope='platform' AND scope_id=0 AND key IN('locale','theme','mailbox_default_quota_bytes','business_timezone','mailbox_domain_limit','mailbox_limit','mailbox_quota_bytes','mailbox_daily_send_limit','ai_default_call_limit') ORDER BY key;

-- name: PlatformGetSetting :one
-- scope: platform

SELECT value,updated_at FROM settings WHERE scope='platform' AND scope_id=0 AND key=?1;

-- name: PlatformUpsertSetting :exec
-- scope: platform

INSERT INTO settings(scope,scope_id,key,value) VALUES('platform',0,?1,?2)
ON CONFLICT(scope,scope_id,key) DO UPDATE SET value=EXCLUDED.value,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: PlatformDeleteSetting :exec
-- scope: platform

DELETE FROM settings WHERE scope='platform' AND scope_id=0 AND key=?1;
