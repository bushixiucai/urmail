-- Platform-only authentication queries; never used as tenant-scoped stores.
-- name: MarkSessionMFAVerified :exec
UPDATE sessions SET mfa_verified_at = now() WHERE id = $1;

-- name: MarkSessionLegacyOnly :exec
UPDATE sessions SET kind='legacy',mfa_verified_at=now() WHERE id=$1;

-- name: GetLegacySession :one
SELECT s.id, s.user_id, s.csrf_token, s.expires_at, s.mfa_verified_at
FROM sessions s
JOIN users u ON u.id = s.user_id AND u.status = 'active'
JOIN platform_admins p ON p.user_id = u.id
LEFT JOIN tenants t ON t.id = s.tenant_id
WHERE s.id = $1 AND s.kind='legacy' AND s.expires_at > now() AND s.credential_version=u.credential_version
  AND s.mfa_verified_at > now() - interval '15 minutes'
  AND (s.tenant_id IS NULL OR t.status IN ('active','pending'));

-- name: CreateApplicationCredential :one
WITH credential AS (
  INSERT INTO application_credentials(user_id,name,token_hash,scopes,expires_at,credential_version)
  SELECT u.id,sqlc.arg(name),sqlc.arg(token_hash),sqlc.arg(scopes),sqlc.arg(expires_at),u.credential_version FROM users u
  WHERE u.id=sqlc.arg(user_id) AND u.status='active' AND u.credential_version=sqlc.arg(expected_version) FOR UPDATE OF u
  RETURNING id,user_id,name,scopes,expires_at,created_at
), audit AS (
  INSERT INTO audit_logs(actor_user_id,action,target_type,target_id,detail)
  SELECT user_id,'application_credential.create','application_credential',id::text,'{}'::jsonb FROM credential
  RETURNING id
)
SELECT credential.* FROM credential CROSS JOIN audit;

-- name: ListApplicationCredentials :many
SELECT id,name,scopes,expires_at,revoked_at,created_at
FROM application_credentials WHERE user_id=$1 ORDER BY id DESC;

-- name: RevokeApplicationCredential :one
WITH revoked AS (
 UPDATE application_credentials SET revoked_at = COALESCE(revoked_at,now())
 WHERE application_credentials.id=$1 AND application_credentials.user_id=$2
 RETURNING id,user_id
), audit AS (
 INSERT INTO audit_logs(actor_user_id,action,target_type,target_id,detail)
 SELECT user_id,'application_credential.revoke','application_credential',id::text,'{}'::jsonb FROM revoked
 RETURNING id
)
SELECT revoked.id FROM revoked CROSS JOIN audit;

-- name: AuthenticateApplicationCredential :one
SELECT a.id,a.user_id,a.scopes
FROM application_credentials a
JOIN users u ON u.id=a.user_id AND u.status='active'
JOIN platform_admins p ON p.user_id=u.id
WHERE a.token_hash=$1 AND u.email=$2 AND a.revoked_at IS NULL AND a.expires_at>now() AND a.credential_version=u.credential_version;

-- name: AuditLegacyCredentialUse :exec
INSERT INTO audit_logs(actor_user_id,action,target_type,target_id,detail)
VALUES($1,$2,'legacy_auth',$3,'{}'::jsonb);
