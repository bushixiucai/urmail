-- Platform queries for brand-host ownership verification (OPS-101).
-- Host lifecycle rows are not tenant-scoped data (platform_*.sql exemption).

-- name: PlatformInsertBrandHostChallenge :exec
INSERT INTO brand_host_challenges(hostname, token_digest, method, expires_at)
VALUES($1, $2, $3, $4)
ON CONFLICT(hostname) DO UPDATE
SET token_digest = EXCLUDED.token_digest, method = EXCLUDED.method,
    expires_at = EXCLUDED.expires_at, consumed_at = NULL, created_at = now();

-- name: PlatformGetBrandHostChallenge :one
SELECT hostname, token_digest, method, expires_at, consumed_at, created_at
FROM brand_host_challenges WHERE hostname = $1;

-- name: PlatformConsumeBrandHostChallenge :execrows
UPDATE brand_host_challenges SET consumed_at = now()
WHERE hostname = $1 AND consumed_at IS NULL AND expires_at > now();

-- name: PlatformDeleteBrandHostChallenge :exec
DELETE FROM brand_host_challenges WHERE hostname = $1;

-- name: PlatformGetBrandHostLifecycle :one
SELECT hostname, brand_id, status, failure_reason, verified_at, cert_issued_at, activated_at, created_at, updated_at
FROM brand_hosts WHERE hostname = $1;

-- name: PlatformSetBrandHostLifecycle :execrows
UPDATE brand_hosts SET status = $2,
  failure_reason = $3,
  verified_at = $4, cert_issued_at = $5, activated_at = $6, updated_at = now()
WHERE hostname = $1;

-- name: PlatformBrandHostCertEligible :one
SELECT EXISTS(
  SELECT 1 FROM brand_hosts h JOIN brands b ON b.id=h.brand_id AND b.status='active'
  WHERE h.hostname = $1 AND h.status IN ('verified','cert-issued','active')
) AS eligible;

-- name: PlatformLockBrandHost :one
SELECT h.* FROM brand_hosts h JOIN brands b ON b.id=h.brand_id AND b.status='active'
WHERE h.hostname=$1 FOR UPDATE OF h;

-- name: PlatformVerifyBrandHostProof :execrows
WITH consumed AS (
 UPDATE brand_host_challenges c SET consumed_at=now()
 WHERE c.hostname=sqlc.arg(hostname) AND c.token_digest=sqlc.arg(token_digest)
 AND c.consumed_at IS NULL AND c.expires_at>now()
 RETURNING c.hostname
)
UPDATE brand_hosts h SET status='verified',verified_at=now(),cert_issued_at=NULL,
 activated_at=NULL,failure_reason=NULL,updated_at=now()
FROM consumed WHERE h.hostname=consumed.hostname AND h.brand_id=sqlc.arg(brand_id)
AND h.updated_at=sqlc.arg(expected_updated_at);

-- name: PlatformBrandHostCertificateTransition :execrows
UPDATE brand_hosts h SET status=sqlc.arg(next_status),failure_reason=sqlc.narg(failure_reason),
 cert_issued_at=CASE WHEN sqlc.arg(next_status)::text IN ('cert-issued','active') THEN now() ELSE h.cert_issued_at END,
 activated_at=CASE WHEN sqlc.arg(next_status)::text='active' THEN now() ELSE NULL END,updated_at=now()
WHERE h.hostname=sqlc.arg(hostname) AND h.brand_id=sqlc.arg(brand_id)
AND h.verified_at=sqlc.arg(verified_at) AND h.status=sqlc.arg(expected_status)
AND EXISTS(SELECT 1 FROM brands b WHERE b.id=h.brand_id AND b.status='active')
AND EXISTS(SELECT 1 FROM platform_admins a JOIN users u ON u.id=a.user_id AND u.status='active' WHERE a.user_id=sqlc.arg(actor_id));

-- name: PlatformTenantBrandMatches :one
SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1 AND brand_id=$2 AND status IN ('active','pending'));

-- name: PlatformBrandHostReserved :one
SELECT EXISTS(SELECT 1 FROM domains WHERE fqdn=$1);
