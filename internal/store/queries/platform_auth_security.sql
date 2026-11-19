-- name: PlatformRotatePanelPassword :one
-- scope: platform

SELECT urmail_rotate_panel_password(sqlc.arg(user_id),sqlc.arg(expected_hash),sqlc.arg(expected_version),sqlc.arg(password_hash),sqlc.arg(keep_session)) AS credential_version;

-- name: PlatformChangePanelMFA :one
-- scope: platform

SELECT urmail_change_panel_mfa(sqlc.arg(user_id),sqlc.arg(expected_version),sqlc.narg(secret),sqlc.arg(enabled),sqlc.arg(codes),sqlc.arg(keep_session)) AS credential_version;

-- name: PlatformRehashPanelPassword :execrows
-- scope: platform

WITH locked AS (SELECT id FROM users WHERE id=sqlc.arg(user_id) AND password_hash=sqlc.arg(expected_hash)), setting AS (SELECT set_config('urmail.password_rehash','true',true) FROM locked)
UPDATE users SET password_hash=sqlc.arg(password_hash) FROM setting WHERE id=sqlc.arg(user_id) AND password_hash=sqlc.arg(expected_hash);
