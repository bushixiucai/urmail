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

-- name: IncrementMailboxDailySent :exec
INSERT INTO mailbox_usage (mailbox_id, tenant_id, day, messages_sent)
VALUES ($1, $2, CURRENT_DATE, 1)
ON CONFLICT (mailbox_id, day)
DO UPDATE SET messages_sent = mailbox_usage.messages_sent + 1;
