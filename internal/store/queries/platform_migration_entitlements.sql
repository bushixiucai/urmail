-- Explicit operator migration grants; never inferred from a paid order.
-- name: PlatformGrantMigrationEntitlement :one
SELECT * FROM urmail_grant_migration_entitlement(sqlc.arg(tenant_id)::bigint,sqlc.arg(manifest_sha256)::text,sqlc.arg(actor_id)::bigint,sqlc.arg(reason)::text);
