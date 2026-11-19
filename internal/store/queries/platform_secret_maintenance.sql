-- name: PlatformCountLegacyMFASecrets :one
-- scope: platform

SELECT count(*) FROM users WHERE mfa_secret IS NOT NULL AND mfa_secret NOT LIKE 'enc:v1:%';

-- name: PlatformLockLegacyMFASecrets :many
-- scope: platform

SELECT id, mfa_secret FROM users
WHERE mfa_secret IS NOT NULL AND mfa_secret NOT LIKE 'enc:v1:%'
ORDER BY id LIMIT ?1;

-- name: PlatformEncryptLegacyMFASecret :execrows
-- scope: platform

UPDATE users SET mfa_secret = ?3 WHERE id = ?1 AND mfa_secret = ?2;
