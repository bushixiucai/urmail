-- Mailbox-scoped retrieval index queries for cross-mail AI Q&A.
-- Every statement repeats the tenant and mailbox predicates; RLS on
-- mailbox_ai_chunks is the authoritative boundary fail-closing without a
-- mailbox principal context.

-- name: MailboxAIChunkMessageStates :many
-- scope: tenant

SELECT folder, folder_uidvalidity, message_uid, content_version, content_digest FROM (
 SELECT folder, folder_uidvalidity, message_uid, content_version, content_digest,
  ROW_NUMBER() OVER (PARTITION BY folder, folder_uidvalidity, message_uid ORDER BY chunk_index) AS rn
 FROM mailbox_ai_chunks
 WHERE tenant_id = ?1 AND mailbox_id = ?2
) WHERE rn = 1;

-- name: DeleteMailboxAIChunkMessages :execrows
-- scope: tenant

DELETE FROM mailbox_ai_chunks
WHERE tenant_id = ?1 AND mailbox_id = ?2 AND folder = ?3
  AND folder_uidvalidity = ?4
  AND CAST(sqlc.arg(message_uids) AS TEXT) <> ''
  AND message_uid IN (SELECT CAST(value AS INTEGER) FROM json_each(CAST(sqlc.arg(message_uids) AS TEXT)));

-- name: DeleteMailboxAIChunkFolder :execrows
-- scope: tenant

DELETE FROM mailbox_ai_chunks
WHERE tenant_id = ?1 AND mailbox_id = ?2 AND folder = ?3;

-- name: PurgeMailboxAIChunks :execrows
-- scope: tenant

DELETE FROM mailbox_ai_chunks
WHERE tenant_id = ?1 AND mailbox_id = ?2;

-- name: InsertMailboxAIChunk :one
-- scope: tenant

INSERT INTO mailbox_ai_chunks
 (tenant_id, mailbox_id, folder, folder_uidvalidity, message_uid,
  chunk_index, content_version, content, content_digest)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)
RETURNING id;

-- name: SearchMailboxAIChunks :many
-- scope: tenant

SELECT id, folder, folder_uidvalidity, message_uid, chunk_index,
       content_version, content, content_digest
FROM mailbox_ai_chunks
WHERE tenant_id = ?1 AND mailbox_id = ?2
  AND content LIKE '%' || ?3 || '%'
ORDER BY id
LIMIT ?4;

-- name: GetMailboxAIChunk :one
-- scope: tenant

SELECT id, folder, folder_uidvalidity, message_uid, chunk_index,
       content_version, content, content_digest
FROM mailbox_ai_chunks
WHERE id = ?3 AND tenant_id = ?1 AND mailbox_id = ?2;

-- name: SearchMailboxAIChunksInScope :many
-- scope: tenant

SELECT c.id,c.folder,c.folder_uidvalidity,c.message_uid,c.chunk_index,c.content_version,c.content,c.content_digest
FROM mailbox_ai_chunks c
WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.mailbox_id=sqlc.arg(mailbox_id)
AND c.content LIKE '%' || sqlc.arg(query) || '%'
AND CAST(sqlc.arg(scope) AS TEXT) <> ''
AND EXISTS (SELECT 1 FROM json_each(sqlc.arg(scope)) AS s
 WHERE s.folder=c.folder AND s.validity=c.folder_uidvalidity AND EXISTS(SELECT 1 FROM json_each(s.uids) je WHERE je.value = c.message_uid))
ORDER BY c.id LIMIT sqlc.arg(result_limit);
