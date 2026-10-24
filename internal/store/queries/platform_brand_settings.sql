-- name: PlatformLockBrandSettingsBrand :one
SELECT id FROM brands WHERE id=$1 FOR UPDATE;

-- name: PlatformListBrandSettings :many
SELECT key,value,updated_at FROM settings WHERE scope='brand' AND scope_id=$1 AND key IN('locale','theme','mailbox_default_quota_bytes','business_timezone') ORDER BY key;

-- name: PlatformUpsertBrandSetting :exec
INSERT INTO settings(scope,scope_id,key,value) VALUES('brand',$1,$2,$3)
ON CONFLICT(scope,scope_id,key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();

-- name: PlatformDeleteBrandSetting :exec
DELETE FROM settings WHERE scope='brand' AND scope_id=$1 AND key=$2;
