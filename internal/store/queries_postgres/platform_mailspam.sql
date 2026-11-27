-- name: PlatformListSpamDeliveryPolicies :many
-- Only the privileged configuration assembler reads the fleet-wide policy.
-- The generated private Sieve file evaluates each rule against the current
-- LMTP envelope recipient; no private result is attached to shared messages.
SELECT m.tenant_id, m.id AS mailbox_id, m.address AS mailbox_address,
       s.scope, s.address AS sender_address, s.domain AS sender_domain, s.list
FROM mailbox_spam_senders s
JOIN mailboxes m ON m.tenant_id=s.tenant_id AND (s.mailbox_id=0 OR s.mailbox_id=m.id)
JOIN tenants t ON t.id=m.tenant_id
JOIN domains d ON d.id=m.domain_id AND d.tenant_id=m.tenant_id
WHERE m.status='active' AND t.status='active' AND d.status='active'
ORDER BY m.tenant_id,m.id,s.scope,s.address,s.domain,s.list
LIMIT 10001;
