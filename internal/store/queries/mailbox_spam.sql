-- MAIL-002: per-mailbox spam feedback, sender allow/deny rules and learned
-- token statistics. Every statement is bound to the tenant_id and mailbox_id
-- of the verified mailbox bearer session (or the milter-resolved recipient)
-- and runs inside a MailboxTx so RLS additionally fails closed on a missing
-- urmail.tenant_id / urmail.mailbox_id context. Cross-mailbox rows are
-- unreachable by construction.

-- name: MailboxSpamInsertFeedback :execrows
INSERT INTO mailbox_spam_feedback(tenant_id,mailbox_id,message_id,folder,verdict,sender_address,sender_domain)
VALUES(sqlc.arg(tenant_id)::bigint,sqlc.arg(mailbox_id)::bigint,sqlc.arg(message_id),sqlc.arg(folder),sqlc.arg(verdict),sqlc.arg(sender_address),sqlc.arg(sender_domain))
ON CONFLICT (tenant_id,mailbox_id,message_id,verdict) DO NOTHING;

-- name: MailboxSpamSetSenderList :execrows
INSERT INTO mailbox_spam_senders(tenant_id,mailbox_id,scope,address,domain,list)
VALUES(sqlc.arg(tenant_id)::bigint,sqlc.arg(mailbox_id)::bigint,'mailbox',sqlc.arg(address),sqlc.arg(domain),sqlc.arg(list))
ON CONFLICT (tenant_id,mailbox_id,address,domain,list) DO UPDATE SET scope='mailbox',updated_at=now();

-- name: MailboxSpamRemoveSenderList :execrows
DELETE FROM mailbox_spam_senders
WHERE tenant_id=sqlc.arg(tenant_id)::bigint AND mailbox_id=sqlc.arg(mailbox_id)::bigint
  AND address=sqlc.arg(address) AND domain=sqlc.arg(domain) AND list=sqlc.arg(list);

-- name: MailboxSpamListSenderRules :many
SELECT scope,address,domain,list FROM mailbox_spam_senders
WHERE tenant_id=sqlc.arg(tenant_id)::bigint
  AND (mailbox_id=0 OR mailbox_id=sqlc.arg(mailbox_id)::bigint)
  AND (address=sqlc.arg(sender_address) OR (address='' AND domain=sqlc.arg(sender_domain)))
  AND (sqlc.arg(sender_address)<>'' OR sqlc.arg(sender_domain)<> '');

-- name: MailboxSpamTokenStats :many
SELECT token,spam_count,ham_count FROM mailbox_spam_tokens
WHERE tenant_id=sqlc.arg(tenant_id)::bigint AND mailbox_id=sqlc.arg(mailbox_id)::bigint;

-- name: MailboxSpamApplyTokenDelta :execrows
INSERT INTO mailbox_spam_tokens(tenant_id,mailbox_id,token,spam_count,ham_count,updated_at)
VALUES(sqlc.arg(tenant_id)::bigint,sqlc.arg(mailbox_id)::bigint,sqlc.arg(token),sqlc.arg(spam_delta)::integer,sqlc.arg(ham_delta)::integer,now())
ON CONFLICT (tenant_id,mailbox_id,token) DO UPDATE SET
 spam_count=GREATEST(0,LEAST(1000000000,mailbox_spam_tokens.spam_count+EXCLUDED.spam_count)),
 ham_count=GREATEST(0,LEAST(1000000000,mailbox_spam_tokens.ham_count+EXCLUDED.ham_count)),
 updated_at=now();

-- name: MailboxSpamCountFeedback :one
SELECT count(*) AS total FROM mailbox_spam_feedback
WHERE tenant_id=sqlc.arg(tenant_id)::bigint AND mailbox_id=sqlc.arg(mailbox_id)::bigint;
