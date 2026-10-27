-- Editable AI drafts owned by one mailbox.
-- A proposed version must be exactly one newer than the stored version.
-- This table has no recipient or send state.

CREATE TABLE mailbox_ai_drafts (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 mailbox_id BIGINT NOT NULL,
 subject TEXT NOT NULL CHECK (length(subject) BETWEEN 1 AND 200 AND position(E'\n' IN subject)=0),
 body TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 20000),
 version BIGINT NOT NULL CHECK (version >= 1),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE (tenant_id, mailbox_id, id),
 FOREIGN KEY (tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE mailbox_ai_drafts ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_ai_drafts FORCE ROW LEVEL SECURITY;
CREATE POLICY mailbox_owner ON mailbox_ai_drafts
 USING (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint AND mailbox_id = NULLIF(current_setting('urmail.mailbox_id', true), '')::bigint)
 WITH CHECK (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint AND mailbox_id = NULLIF(current_setting('urmail.mailbox_id', true), '')::bigint);
GRANT SELECT, INSERT, UPDATE, DELETE ON mailbox_ai_drafts TO urmail_app, urmail_platform;

-- The platform runtime role owns maintenance tables (restore quarantine,
-- fixture maintenance, TRUNCATE); same contract as 0028.
ALTER TABLE mailbox_ai_drafts OWNER TO urmail_platform;
