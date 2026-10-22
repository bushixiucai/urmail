-- Platform-side daily-sent usage queries used by the SMTP policy service
-- (internal/mail/policy). The policy service resolves the tenant from the
-- mailbox address itself and runs outside a tenant context, so these queries
-- are exempt from the tenant_id predicate per AGENTS.md and therefore live in
-- a platform_*.sql file (M3 review P1-2).
-- (moved verbatim from usage.sql)

-- name: GetMailboxDailySent :one
SELECT COALESCE(u.messages_sent, 0) AS messages_sent, m.id AS mailbox_id, m.tenant_id
FROM mailboxes m
LEFT JOIN mailbox_usage u ON m.id = u.mailbox_id AND u.day = CURRENT_DATE
WHERE m.address = $1 AND m.status = 'active';

-- name: GetMailboxDailySentForUpdate :one
-- Locks the mailbox row to serialize concurrent daily sent quota evaluation
SELECT COALESCE(u.messages_sent, 0) AS messages_sent, m.id AS mailbox_id, m.tenant_id
FROM mailboxes m
LEFT JOIN mailbox_usage u ON m.id = u.mailbox_id AND u.day = CURRENT_DATE
WHERE m.address = $1 AND m.status = 'active'
FOR UPDATE OF m;

-- name: IncrementMailboxDailySent :one
-- Atomic check-and-increment: the counter is only advanced while it stays
-- below the daily limit ($3); returns pgx.ErrNoRows when the limit is
-- already reached.
INSERT INTO mailbox_usage (mailbox_id, tenant_id, day, messages_sent)
VALUES ($1, $2, CURRENT_DATE, 1)
ON CONFLICT (mailbox_id, day)
DO UPDATE SET messages_sent = mailbox_usage.messages_sent + 1
WHERE mailbox_usage.messages_sent < $3
RETURNING messages_sent;
