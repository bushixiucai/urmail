-- Mailbox-scoped retrieval index backing cross-mail AI Q&A.
-- Chunks copy searchable mail text so that retrieval never needs a live IMAP
-- round trip, and every row carries the full source coordinates (folder, uid,
-- uidvalidity) plus an indexed-content version for incremental reindexing.
-- Authorization stays with the mailbox principal: RLS fail-closes without the
-- urmail.mailbox_id setting and every store query repeats the predicates.

CREATE TABLE mailbox_ai_chunks (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 mailbox_id BIGINT NOT NULL,
 folder TEXT NOT NULL CHECK (length(folder) BETWEEN 1 AND 500),
 folder_uidvalidity BIGINT NOT NULL CHECK (folder_uidvalidity BETWEEN 1 AND 4294967295),
 message_uid BIGINT NOT NULL CHECK (message_uid BETWEEN 1 AND 4294967295),
 chunk_index INT NOT NULL CHECK (chunk_index >= 0),
 content_version INT NOT NULL CHECK (content_version >= 1),
 content TEXT NOT NULL CHECK (length(content) BETWEEN 1 AND 2000),
 content_digest TEXT NOT NULL CHECK (content_digest ~ '^[a-f0-9]{64}$'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE (tenant_id, mailbox_id, folder, folder_uidvalidity, message_uid, chunk_index),
 FOREIGN KEY (tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE mailbox_ai_chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_ai_chunks FORCE ROW LEVEL SECURITY;
CREATE POLICY mailbox_owner ON mailbox_ai_chunks
 USING (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint AND mailbox_id = NULLIF(current_setting('urmail.mailbox_id', true), '')::bigint)
 WITH CHECK (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint AND mailbox_id = NULLIF(current_setting('urmail.mailbox_id', true), '')::bigint);
GRANT SELECT, INSERT, UPDATE, DELETE ON mailbox_ai_chunks TO urmail_app, urmail_platform;
CREATE INDEX mailbox_ai_chunks_message_idx
 ON mailbox_ai_chunks(tenant_id, mailbox_id, folder, folder_uidvalidity, message_uid);

-- The platform runtime role owns maintenance tables (restore quarantine,
-- fixture maintenance, TRUNCATE); same contract as 0028.
ALTER TABLE mailbox_ai_chunks OWNER TO urmail_platform;
