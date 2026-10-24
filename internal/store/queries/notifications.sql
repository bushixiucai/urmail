-- name: ListTenantNotifications :many
SELECT id,kind,message_key,params,channel,state,created_at,read_at FROM notification_outbox
WHERE tenant_id=$1 AND (user_id IS NULL OR user_id=nullif(current_setting('urmail.user_id',true),'')::bigint)
AND (sqlc.arg(before_id)::bigint=0 OR id<sqlc.arg(before_id)) ORDER BY id DESC LIMIT 50;
