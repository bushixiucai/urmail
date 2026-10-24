-- name: PlatformListSettings :many
SELECT key,value,updated_at FROM settings WHERE scope='platform' AND scope_id=0 AND key IN('locale','theme','mailbox_default_quota_bytes','business_timezone') ORDER BY key;

-- name: PlatformUpsertSetting :exec
INSERT INTO settings(scope,scope_id,key,value) VALUES('platform',0,$1,$2)
ON CONFLICT(scope,scope_id,key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();

-- name: PlatformDeleteSetting :exec
DELETE FROM settings WHERE scope='platform' AND scope_id=0 AND key=$1;
