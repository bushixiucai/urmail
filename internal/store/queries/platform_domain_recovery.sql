-- These queries intentionally use only columns already present in schema 25.
-- Recovery must work before migration 0032 and must not require the new daemon.

-- name: PlatformGetDomainForRecovery :one
-- scope: platform

SELECT id,tenant_id,fqdn,status,verification_token,verified_at
FROM domains WHERE tenant_id=?1 AND id=?2;

-- name: PlatformRotateDomainRecoveryToken :execrows
-- scope: platform

UPDATE domains SET verification_token=?3
WHERE tenant_id=?1 AND id=?2 AND fqdn=?4;

-- name: PlatformConfirmDomainRecovery :execrows
-- scope: platform

UPDATE domains SET verified_at=(CAST(strftime('%s','now') AS INTEGER)*1000000), status='active'
WHERE tenant_id=?1 AND id=?2 AND fqdn=?3 AND verification_token=?4;
