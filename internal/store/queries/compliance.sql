-- Archive / legal hold (tenant-scoped). M3 review P2-3: the tables existed
-- without any queries; writes are wired by the platform side later.

-- name: InsertArchiveEntry :one
INSERT INTO archive_entries (
    tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr, received_at, size_bytes, legal_hold
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
) RETURNING id, tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr, received_at, size_bytes, legal_hold, deleted_at, created_at, raw_sha256;

-- name: ListArchiveEntriesByTenant :many
SELECT id, tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr, received_at, size_bytes, legal_hold, deleted_at, created_at, raw_sha256
FROM archive_entries
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY received_at DESC, id DESC
LIMIT $2 OFFSET $3;

-- name: GetArchiveEntryByID :one
SELECT id, tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr, received_at, size_bytes, legal_hold, deleted_at, created_at, raw_sha256
FROM archive_entries
WHERE tenant_id = $1 AND id = $2;

-- name: GetArchiveEntryLegalHold :one
SELECT legal_hold
FROM archive_entries
WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL;

-- name: SetArchiveEntryLegalHold :one
UPDATE archive_entries
SET legal_hold = true, deleted_at = NULL
WHERE tenant_id = $1 AND id = $2
RETURNING id, tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr, received_at, size_bytes, legal_hold, deleted_at, created_at, raw_sha256;

-- name: ClearArchiveEntryLegalHold :one
UPDATE archive_entries
SET legal_hold = false
WHERE tenant_id = $1 AND id = $2
RETURNING id, tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr, received_at, size_bytes, legal_hold, deleted_at, created_at, raw_sha256;

-- name: SoftDeleteArchiveEntry :one
SELECT result::bigint AS id FROM urmail_soft_delete_archive(@tenant_id::bigint,@id::bigint) AS result WHERE result IS NOT NULL;

-- name: ListMailboxesWithLegalHold :many
SELECT DISTINCT mailbox_id
FROM archive_entries
WHERE tenant_id = $1 AND legal_hold = true AND mailbox_id IS NOT NULL AND deleted_at IS NULL;
