-- name: PlatformCompleteRecall :one
WITH completed AS (
 UPDATE recall_requests r SET status=$3,reason=$4,completed_at=now() WHERE r.tenant_id=$1 AND r.id=$2 AND r.status='pending' RETURNING r.*
), audited AS (
 INSERT INTO audit_logs(tenant_id,actor_user_id,action,target_type,target_id,detail)
 SELECT c.tenant_id,c.requested_by,'compliance.recall.complete','recall',c.id::text,jsonb_build_object('status',c.status,'scope','local_mailbox') FROM completed c RETURNING id
)
SELECT completed.* FROM completed CROSS JOIN audited;

-- name: PlatformLockRecallForExpunge :one
SELECT r.*,a.id AS archive_entry_id,a.legal_hold,a.deleted_at AS archive_deleted_at,a.message_id AS archive_message_id,a.mailbox_id AS archive_mailbox_id,m.address,m.status AS mailbox_status,t.status AS tenant_status,d.status AS domain_status
FROM recall_requests r
JOIN archive_entries a ON a.id=r.archive_entry_id AND a.tenant_id=r.tenant_id
LEFT JOIN mailboxes m ON m.id=r.mailbox_id AND m.tenant_id=r.tenant_id
JOIN tenants t ON t.id=r.tenant_id
LEFT JOIN domains d ON d.id=m.domain_id AND d.tenant_id=r.tenant_id
WHERE r.tenant_id=$1 AND r.id=$2 FOR UPDATE OF r,a;

-- name: PlatformLockRecallTenant :exec
SELECT pg_advisory_xact_lock($1::bigint);
-- name: PlatformRecallHoldConflict :one
-- ARC-01: expunge matches on Message-ID, so recalling one archived copy of a
-- message must be refused while another copy of the same Message-ID for the
-- same mailbox is under legal hold -- the mailbox expunge would destroy the
-- held copy's Maildir original too. Exempt from tenant_id predicate per
-- AGENTS.md (platform_*.sql); the agent is the caller.
SELECT count(*) > 0 AS conflict
FROM archive_entries
WHERE tenant_id = $1
  AND mailbox_id = $2
  AND message_id = $3
  AND id <> $4
  AND legal_hold
  AND deleted_at IS NULL;

