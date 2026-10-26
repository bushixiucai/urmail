-- Service-only operations: authenticated Roundcube plugin; never panel identity.
-- name: PlatformRecordRoundcubeNonce :exec
INSERT INTO roundcube_bridge_requests(nonce,expires_at) VALUES(sqlc.arg(nonce),now()+interval '5 minutes');

-- name: PlatformCreateMailboxSession :one
INSERT INTO mailbox_sessions(token_hash,tenant_id,mailbox_id,password_version,expires_at)
SELECT sqlc.arg(token_hash),m.tenant_id,m.id,md5(m.password_hash),now()+interval '30 minutes'
FROM mailboxes m JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE m.address=sqlc.arg(address) AND m.status='active' AND t.status='active' AND d.status='active'
RETURNING token_hash,tenant_id,mailbox_id,password_version,expires_at,revoked_at,created_at;

-- name: PlatformGetMailboxSession :one
SELECT s.token_hash,s.tenant_id,s.mailbox_id,s.expires_at,m.address,s.password_version FROM mailbox_sessions s
JOIN mailboxes m ON m.tenant_id=s.tenant_id AND m.id=s.mailbox_id
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE s.token_hash=sqlc.arg(token_hash) AND s.revoked_at IS NULL AND s.expires_at>now()
AND s.password_version=md5(m.password_hash) AND m.status='active' AND t.status='active' AND d.status='active';

-- name: PlatformRevokeMailboxSession :exec
UPDATE mailbox_sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE token_hash=sqlc.arg(token_hash);

-- name: PlatformCreateMailboxTicket :one
INSERT INTO mailbox_operation_tickets(token_hash,tenant_id,mailbox_id,session_hash,scope,folder,message_uid,uid_validity,expires_at)
SELECT sqlc.arg(token_hash),s.tenant_id,s.mailbox_id,s.token_hash,sqlc.arg(scope),sqlc.arg(folder),sqlc.arg(message_uid),sqlc.arg(uid_validity),now()+interval '1 minute'
FROM mailbox_sessions s JOIN mailboxes m ON m.tenant_id=s.tenant_id AND m.id=s.mailbox_id
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE s.token_hash=sqlc.arg(session_hash) AND s.revoked_at IS NULL AND s.expires_at>now() AND s.password_version=md5(m.password_hash)
AND m.status='active' AND t.status='active' AND d.status='active'
RETURNING token_hash,tenant_id,mailbox_id,session_hash,scope,folder,message_uid,uid_validity,expires_at,consumed_at,created_at,kind;

-- name: PlatformConsumeMailboxTicket :one
UPDATE mailbox_operation_tickets ticket SET consumed_at=now()
FROM mailbox_sessions s JOIN mailboxes m ON m.tenant_id=s.tenant_id AND m.id=s.mailbox_id
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE ticket.token_hash=sqlc.arg(token_hash) AND ticket.scope=sqlc.arg(scope) AND ticket.folder=sqlc.arg(folder) AND ticket.message_uid=sqlc.arg(message_uid) AND ticket.uid_validity=sqlc.arg(uid_validity)
AND ticket.session_hash=s.token_hash AND ticket.tenant_id=s.tenant_id AND ticket.mailbox_id=s.mailbox_id
AND ticket.kind='operation' AND ticket.consumed_at IS NULL AND ticket.expires_at>now() AND s.revoked_at IS NULL AND s.expires_at>now()
AND s.password_version=md5(m.password_hash) AND m.status='active' AND t.status='active' AND d.status='active'
RETURNING ticket.tenant_id,ticket.mailbox_id,ticket.scope,ticket.folder,ticket.message_uid,ticket.uid_validity,m.address,s.token_hash AS session_hash;

-- name: PlatformPruneRoundcubeRequests :exec
DELETE FROM roundcube_bridge_requests WHERE expires_at<now();

-- name: PlatformPruneMailboxTickets :exec
DELETE FROM mailbox_operation_tickets WHERE expires_at<now()-interval '1 day';

-- name: PlatformPruneMailboxSessions :exec
DELETE FROM mailbox_sessions s WHERE s.expires_at<now()-interval '1 day'
AND NOT EXISTS(SELECT 1 FROM mailbox_operation_tickets ticket WHERE ticket.session_hash=s.token_hash AND ticket.tenant_id=s.tenant_id);


-- name: PlatformCreateSieveBridge :exec
INSERT INTO mailbox_operation_tickets(token_hash,tenant_id,mailbox_id,session_hash,scope,kind,expires_at)
SELECT sqlc.arg(token_hash),s.tenant_id,s.mailbox_id,s.token_hash,'sieve','sieve_bridge',LEAST(s.expires_at,now()+interval '30 minutes')
FROM mailbox_sessions s JOIN mailbox_operation_tickets ticket ON ticket.session_hash=s.token_hash AND ticket.tenant_id=s.tenant_id AND ticket.mailbox_id=s.mailbox_id
WHERE ticket.token_hash=sqlc.arg(source_ticket) AND ticket.kind='operation' AND ticket.scope='sieve' AND ticket.consumed_at IS NOT NULL
AND s.revoked_at IS NULL AND s.expires_at>now();

-- name: PlatformGetSieveBridge :one
SELECT ticket.tenant_id,ticket.mailbox_id,m.address,ticket.expires_at FROM mailbox_operation_tickets ticket
JOIN mailbox_sessions s ON s.token_hash=ticket.session_hash AND s.tenant_id=ticket.tenant_id AND s.mailbox_id=ticket.mailbox_id
JOIN mailboxes m ON m.tenant_id=s.tenant_id AND m.id=s.mailbox_id
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE ticket.token_hash=$1 AND ticket.kind='sieve_bridge' AND ticket.scope='sieve' AND ticket.consumed_at IS NULL AND ticket.expires_at>now()
AND s.revoked_at IS NULL AND s.expires_at>now() AND s.password_version=md5(m.password_hash) AND m.status='active' AND t.status='active' AND d.status='active' FOR UPDATE OF m,s,ticket;

-- name: PlatformRevokeSieveBridge :exec
UPDATE mailbox_operation_tickets SET consumed_at=now() WHERE token_hash=$1 AND kind='sieve_bridge';

-- name: PlatformGetMailboxSessionForRevocation :one
SELECT token_hash,tenant_id,mailbox_id FROM mailbox_sessions WHERE token_hash=$1;

-- name: PlatformCreateVerifiedMailboxSession :one
INSERT INTO mailbox_sessions(token_hash,tenant_id,mailbox_id,password_version,expires_at)
SELECT sqlc.arg(token_hash),m.tenant_id,m.id,md5(m.password_hash),now()+interval '30 minutes'
FROM mailboxes m JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE m.address=sqlc.arg(address) AND m.password_hash=sqlc.arg(expected_hash)
AND m.status='active' AND t.status='active' AND d.status='active'
FOR UPDATE OF m
RETURNING token_hash,tenant_id,mailbox_id,password_version,expires_at,revoked_at,created_at;
