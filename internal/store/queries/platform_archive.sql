-- Platform-side archive writes. The archive pipe runs outside a tenant
-- transaction, so these queries live in a platform_*.sql file.

-- name: PlatformGetMailboxByAddress :one
SELECT id, tenant_id, address
FROM mailboxes
WHERE address = $1 AND status = 'active';

-- name: PlatformInsertArchiveEntry :exec
INSERT INTO archive_entries
    (tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr, received_at, size_bytes, legal_hold)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, false)
ON CONFLICT ON CONSTRAINT archive_entries_tenant_mailbox_message_uniq DO NOTHING;
