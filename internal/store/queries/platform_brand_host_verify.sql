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
  SELECT 1 FROM brand_hosts
  WHERE hostname = $1 AND status IN ('verified','cert-issued','active')
) AS eligible;
