-- Platform queries for brand-host ownership verification (OPS-101).
-- Host lifecycle rows are not tenant-scoped data (platform_*.sql exemption).

-- name: PlatformInsertBrandHostChallenge :exec
-- scope: platform

INSERT INTO brand_host_challenges(hostname, token_digest, method, expires_at)
VALUES(?1, ?2, ?3, ?4)
ON CONFLICT(hostname) DO UPDATE
SET token_digest = EXCLUDED.token_digest, method = EXCLUDED.method,
    expires_at = EXCLUDED.expires_at, consumed_at = NULL, created_at = (CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: PlatformGetBrandHostChallenge :one
-- scope: platform

SELECT hostname, token_digest, method, expires_at, consumed_at, created_at
FROM brand_host_challenges WHERE hostname = ?1;

-- name: PlatformConsumeBrandHostChallenge :execrows
-- scope: platform

UPDATE brand_host_challenges SET consumed_at = (CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE hostname = ?1 AND consumed_at IS NULL AND expires_at > (CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: PlatformDeleteBrandHostChallenge :exec
-- scope: platform

DELETE FROM brand_host_challenges WHERE hostname = ?1;

-- name: PlatformGetBrandHostLifecycle :one
-- scope: platform

SELECT hostname, brand_id, status, failure_reason, verified_at, cert_issued_at, activated_at, created_at, updated_at
FROM brand_hosts WHERE hostname = ?1;

-- name: PlatformSetBrandHostLifecycle :execrows
-- scope: platform

UPDATE brand_hosts SET status = ?2,
  failure_reason = ?3,
  verified_at = ?4, cert_issued_at = ?5, activated_at = ?6, updated_at = (CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE hostname = ?1;

-- name: PlatformBrandHostCertEligible :one
-- scope: platform

SELECT EXISTS(
  SELECT 1 FROM brand_hosts h JOIN brands b ON b.id=h.brand_id AND b.status='active'
  WHERE h.hostname = ?1 AND h.status IN ('verified','cert-issued','active')
) AS eligible;

-- name: PlatformLockBrandHost :one
-- scope: platform

SELECT h.* FROM brand_hosts h JOIN brands b ON b.id=h.brand_id AND b.status='active'
WHERE h.hostname=?1;

-- scope: platform

-- name: PlatformVerifyBrandHostConsumeChallenge :one
-- scope: platform

UPDATE brand_host_challenges c SET consumed_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE c.hostname=sqlc.arg(hostname) AND c.token_digest=sqlc.arg(token_digest)
 AND c.consumed_at IS NULL AND c.expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000)
RETURNING c.hostname;

-- name: PlatformVerifyBrandHostMark :exec
-- scope: platform

UPDATE brand_hosts h SET status='verified', verified_at=(CAST(strftime('%s','now') AS INTEGER)*1000000), cert_issued_at=NULL,
 activated_at=NULL, failure_reason=NULL, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE h.hostname=sqlc.arg(hostname) AND h.brand_id=sqlc.arg(brand_id)
 AND h.updated_at=sqlc.arg(expected_updated_at)
 AND EXISTS(SELECT 1 FROM brand_host_challenges c WHERE c.hostname=h.hostname AND c.consumed_at IS NOT NULL);
-- name: PlatformBrandHostCertificateTransition :execrows
-- scope: platform

UPDATE brand_hosts h SET status=sqlc.arg(next_status),failure_reason=sqlc.narg(failure_reason),
 cert_issued_at=CASE WHEN sqlc.arg(next_status) IN ('cert-issued','active') THEN (CAST(strftime('%s','now') AS INTEGER)*1000000) ELSE h.cert_issued_at END,
 activated_at=CASE WHEN sqlc.arg(next_status)='active' THEN (CAST(strftime('%s','now') AS INTEGER)*1000000) ELSE NULL END,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE h.hostname=sqlc.arg(hostname) AND h.brand_id=sqlc.arg(brand_id)
AND h.verified_at=sqlc.arg(verified_at) AND h.status=sqlc.arg(expected_status)
AND EXISTS(SELECT 1 FROM brands b WHERE b.id=h.brand_id AND b.status='active')
AND EXISTS(SELECT 1 FROM platform_admins a JOIN users u ON u.id=a.user_id AND u.status='active' WHERE a.user_id=sqlc.arg(actor_id));

-- name: PlatformTenantBrandMatches :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM tenants WHERE id=?1 AND brand_id=?2 AND status IN ('active','pending'));

-- name: PlatformBrandHostReserved :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM domains WHERE fqdn=?1);
