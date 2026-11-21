-- Tenant-scoped marketing suppression operations.

-- name: AddMarketingSuppression :one
INSERT INTO marketing_suppressions (tenant_id, email, reason)
VALUES ($1, $2, $3)
ON CONFLICT (tenant_id, email) DO UPDATE
SET reason = CASE
    WHEN marketing_suppressions.reason IN ('unsubscribe', 'bounce', 'complaint') THEN marketing_suppressions.reason
    ELSE EXCLUDED.reason
END
RETURNING *;

-- name: CheckMarketingSuppression :one
SELECT EXISTS (SELECT 1 FROM marketing_suppressions WHERE tenant_id = $1 AND email = $2) AS suppressed;

-- name: RemoveMarketingSuppression :execrows
DELETE FROM marketing_suppressions WHERE tenant_id = $1 AND email = $2 AND reason = 'manual';

-- name: ListMarketingSuppressions :many
SELECT * FROM marketing_suppressions WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 500;

-- name: GetMarketingSuppression :one
SELECT * FROM marketing_suppressions WHERE tenant_id = $1 AND email = $2;
