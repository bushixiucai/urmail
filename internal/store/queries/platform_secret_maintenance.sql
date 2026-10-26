-- name: PlatformCountLegacyMFASecrets :one
SELECT count(*) FROM users WHERE mfa_secret IS NOT NULL AND mfa_secret NOT LIKE 'enc:v1:%';

-- name: PlatformLockLegacyMFASecrets :many
SELECT id, mfa_secret FROM users
WHERE mfa_secret IS NOT NULL AND mfa_secret NOT LIKE 'enc:v1:%'
ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED;

-- name: PlatformEncryptLegacyMFASecret :execrows
UPDATE users SET mfa_secret = $3 WHERE id = $1 AND mfa_secret = $2;
