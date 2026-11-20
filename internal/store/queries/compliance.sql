-- Archive / legal hold (tenant-scoped). M3 review P2-3: the tables existed
-- without any queries; writes are wired by the platform side later.

-- name: InsertArchiveEntry :one
-- scope: tenant

INSERT INTO archive_entries (
    tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr, received_at, size_bytes, legal_hold
) VALUES (
    ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10
) RETURNING id, tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr, received_at, size_bytes, legal_hold, deleted_at, created_at, raw_sha256;

-- name: ListArchiveEntriesByTenant :many
-- scope: tenant

SELECT id, tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr, received_at, size_bytes, legal_hold, deleted_at, created_at, raw_sha256
FROM archive_entries
WHERE tenant_id = ?1 AND deleted_at IS NULL
ORDER BY received_at DESC, id DESC
LIMIT ?2 OFFSET ?3;

-- name: GetArchiveEntryByID :one
-- scope: tenant

SELECT id, tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr, received_at, size_bytes, legal_hold, deleted_at, created_at, raw_sha256
FROM archive_entries
WHERE tenant_id = ?1 AND id = ?2;

-- name: GetArchiveEntryLegalHold :one
-- scope: tenant

SELECT legal_hold
FROM archive_entries
WHERE tenant_id = ?1 AND id = ?2 AND deleted_at IS NULL;

-- name: SetArchiveEntryLegalHold :one
-- scope: tenant

UPDATE archive_entries
SET legal_hold = true, deleted_at = NULL
WHERE tenant_id = ?1 AND id = ?2
RETURNING id, tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr, received_at, size_bytes, legal_hold, deleted_at, created_at, raw_sha256;

-- name: ClearArchiveEntryLegalHold :one
-- scope: tenant

UPDATE archive_entries
SET legal_hold = false
WHERE tenant_id = ?1 AND id = ?2
RETURNING id, tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr, received_at, size_bytes, legal_hold, deleted_at, created_at, raw_sha256;

-- SoftDeleteArchiveEntry 在 S4 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- urmail_soft_delete_archive = 行选择 + 软删 + 清 retention_preview（事务；未命中返回 pgx.ErrNoRows）。

-- name: ListMailboxesWithLegalHold :many
-- scope: tenant

SELECT DISTINCT mailbox_id
FROM archive_entries
WHERE tenant_id = ?1 AND legal_hold = true AND mailbox_id IS NOT NULL AND deleted_at IS NULL;
