-- PlatformRotatePanelPassword 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- urmail_rotate_panel_password = 凭据/会话校验 + 轮换 + 撤销其余会话 + 审计（事务）。

-- PlatformChangePanelMFA 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- urmail_change_panel_mfa = 同上（MFA 开关路径）。

-- name: PlatformRehashPanelPassword :execrows
-- scope: platform

WITH locked AS (SELECT id FROM users WHERE id=sqlc.arg(user_id) AND password_hash=sqlc.arg(expected_hash)), setting AS (SELECT set_config('urmail.password_rehash','true',true) FROM locked)
UPDATE users SET password_hash=sqlc.arg(password_hash) FROM setting WHERE id=sqlc.arg(user_id) AND password_hash=sqlc.arg(expected_hash);
