-- name: PlatformRotatePanelPassword :one
SELECT urmail_rotate_panel_password(sqlc.arg(user_id)::bigint,sqlc.arg(expected_hash)::text,sqlc.arg(expected_version)::bigint,sqlc.arg(password_hash)::text,sqlc.arg(keep_session)::text)::bigint AS credential_version;

-- name: PlatformChangePanelMFA :one
SELECT urmail_change_panel_mfa(sqlc.arg(user_id)::bigint,sqlc.arg(expected_version)::bigint,sqlc.narg(secret)::text,sqlc.arg(enabled)::boolean,sqlc.arg(codes)::jsonb,sqlc.arg(keep_session)::text)::bigint AS credential_version;

-- name: PlatformRehashPanelPassword :execrows
WITH locked AS (SELECT id FROM users WHERE id=sqlc.arg(user_id)::bigint AND password_hash=sqlc.arg(expected_hash)::text FOR UPDATE), setting AS (SELECT set_config('urmail.password_rehash','true',true) FROM locked)
UPDATE users SET password_hash=sqlc.arg(password_hash)::text FROM setting WHERE id=sqlc.arg(user_id)::bigint AND password_hash=sqlc.arg(expected_hash)::text;
