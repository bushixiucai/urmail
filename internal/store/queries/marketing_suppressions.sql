-- Tenant-scoped marketing suppression operations.

-- name: AddMarketingSuppression :one
-- scope: tenant

INSERT INTO marketing_suppressions (tenant_id, email, reason)
VALUES (?1, ?2, ?3)
ON CONFLICT (tenant_id, email) DO UPDATE
SET reason = CASE
    WHEN marketing_suppressions.reason IN ('unsubscribe', 'bounce', 'complaint') THEN marketing_suppressions.reason
    ELSE EXCLUDED.reason
END
RETURNING *;

-- name: CheckMarketingSuppression :one
-- scope: tenant

SELECT EXISTS (SELECT 1 FROM marketing_suppressions WHERE tenant_id = ?1 AND email = ?2) AS suppressed;

-- name: RemoveMarketingSuppression :execrows
-- scope: tenant

DELETE FROM marketing_suppressions WHERE tenant_id = ?1 AND email = ?2 AND reason = 'manual';

-- name: ListMarketingSuppressions :many
-- scope: tenant

SELECT * FROM marketing_suppressions WHERE tenant_id = ?1 ORDER BY created_at DESC LIMIT 500;

-- name: GetMarketingSuppression :one
-- scope: tenant

SELECT * FROM marketing_suppressions WHERE tenant_id = ?1 AND email = ?2;
