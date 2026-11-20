-- name: PlatformCompleteRecall :one
-- scope: platform

UPDATE recall_requests SET status=?3, reason=?4, completed_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND id=?2 AND status='pending'
RETURNING id, tenant_id, archive_entry_id, mailbox_id, message_id, requested_by, status, reason,
 local_result, completed_at, created_at, updated_at, preview_actor_id, preview_token, preview_digest,
 preview_snapshot, preview_expires_at, confirmed_at, selected_copy_keys, attempt_epoch, result_history;
-- name: PlatformLockRecallForExpunge :one
-- scope: platform

SELECT r.*,a.id AS archive_entry_id,a.legal_hold,a.deleted_at AS archive_deleted_at,a.message_id AS archive_message_id,a.mailbox_id AS archive_mailbox_id,m.address,m.status AS mailbox_status,t.status AS tenant_status,d.status AS domain_status
FROM recall_requests r
JOIN archive_entries a ON a.id=r.archive_entry_id AND a.tenant_id=r.tenant_id
LEFT JOIN mailboxes m ON m.id=r.mailbox_id AND m.tenant_id=r.tenant_id
JOIN tenants t ON t.id=r.tenant_id
LEFT JOIN domains d ON d.id=m.domain_id AND d.tenant_id=r.tenant_id
WHERE r.tenant_id=?1 AND r.id=?2;

-- name: PlatformLockRecallTenant :exec
-- scope: platform

SELECT pg_advisory_xact_lock(?1);
-- name: PlatformRecallHoldConflict :one
-- scope: platform

-- ARC-01: expunge matches on Message-ID, so recalling one archived copy of a
-- message must be refused while another copy of the same Message-ID for the
-- same mailbox is under legal hold -- the mailbox expunge would destroy the
-- held copy's Maildir original too. Exempt from tenant_id predicate per
-- AGENTS.md (platform_*.sql); the agent is the caller.
SELECT count(*) > 0 AS conflict
FROM archive_entries
WHERE tenant_id = ?1
  AND mailbox_id = ?2
  AND message_id = ?3
  AND id <> ?4
  AND legal_hold
  AND deleted_at IS NULL;

