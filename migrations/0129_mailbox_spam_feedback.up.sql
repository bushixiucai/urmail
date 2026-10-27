-- MAIL-002: server-side anti-spam to user-correction closed loop.
-- Three per-mailbox tables, all tenant-scoped and fail-closed under RLS:
-- feedback rows (user verdicts), sender allow/deny rules (follow-up policy)
-- and learned token statistics (training input). No table is readable or
-- writable by urmail_dovecot or urmail_app without the matching
-- urmail.tenant_id / urmail.mailbox_id session settings, so one mailbox
-- principal can never change another's rules or training input.

CREATE TABLE mailbox_spam_feedback (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id BIGINT NOT NULL,
 mailbox_id BIGINT NOT NULL,
 message_id TEXT NOT NULL CHECK (length(message_id) BETWEEN 1 AND 998
   AND position(E'\n' IN message_id) = 0 AND position(E'\r' IN message_id) = 0),
 folder TEXT NOT NULL CHECK (length(folder) BETWEEN 1 AND 1024),
 verdict TEXT NOT NULL CHECK (verdict IN ('spam','ham')),
 sender_address TEXT NOT NULL DEFAULT '' CHECK (length(sender_address) <= 254),
 sender_domain TEXT NOT NULL DEFAULT '' CHECK (length(sender_domain) <= 254),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 FOREIGN KEY (tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE mailbox_spam_feedback ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_spam_feedback FORCE ROW LEVEL SECURITY;
CREATE POLICY mailbox_owner ON mailbox_spam_feedback
 USING (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint
   AND mailbox_id = NULLIF(current_setting('urmail.mailbox_id', true), '')::bigint)
 WITH CHECK (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint
   AND mailbox_id = NULLIF(current_setting('urmail.mailbox_id', true), '')::bigint);
GRANT SELECT, INSERT, UPDATE, DELETE ON mailbox_spam_feedback TO urmail_app, urmail_platform;
REVOKE ALL ON mailbox_spam_feedback FROM PUBLIC, urmail_dovecot;
-- Re-reporting the same message with the same verdict is idempotent.
CREATE UNIQUE INDEX mailbox_spam_feedback_message_idx
 ON mailbox_spam_feedback(tenant_id, mailbox_id, message_id, verdict);
CREATE INDEX mailbox_spam_feedback_recent_idx
 ON mailbox_spam_feedback(tenant_id, mailbox_id, created_at DESC);

-- Sender rules. mailbox_id > 0 with scope='mailbox' is the per-mailbox policy
-- the feedback API writes; scope='tenant' rows (mailbox_id = 0) are reserved
-- for operator-managed tenant-wide entries. V1 code only writes mailbox
-- scope; the read policy already exposes tenant rows so both are consulted
-- by the verdict engine.
CREATE TABLE mailbox_spam_senders (
 tenant_id BIGINT NOT NULL,
 mailbox_id BIGINT NOT NULL DEFAULT 0,
 scope TEXT NOT NULL CHECK (scope IN ('mailbox','tenant')),
 address TEXT NOT NULL DEFAULT '' CHECK (length(address) <= 254),
 domain TEXT NOT NULL DEFAULT '' CHECK (length(domain) <= 254),
 list TEXT NOT NULL CHECK (list IN ('allow','deny')),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id, mailbox_id, address, domain, list),
 FOREIGN KEY (tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id) ON DELETE CASCADE,
 CHECK ((scope = 'mailbox' AND mailbox_id > 0) OR (scope = 'tenant' AND mailbox_id = 0)),
 CHECK (address <> '' OR domain <> '')
);
ALTER TABLE mailbox_spam_senders ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_spam_senders FORCE ROW LEVEL SECURITY;
-- Reads see this tenant's own mailbox rows plus tenant-wide rows; writes are
-- restricted to the authenticated mailbox's own scope.
CREATE POLICY mailbox_scope ON mailbox_spam_senders
 USING (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint
   AND (mailbox_id = 0
     OR mailbox_id = NULLIF(current_setting('urmail.mailbox_id', true), '')::bigint))
 WITH CHECK (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint
   AND mailbox_id = NULLIF(current_setting('urmail.mailbox_id', true), '')::bigint);
GRANT SELECT, INSERT, UPDATE, DELETE ON mailbox_spam_senders TO urmail_app, urmail_platform;
REVOKE ALL ON mailbox_spam_senders FROM PUBLIC, urmail_dovecot;

-- Learned token statistics per mailbox. Counts saturate instead of wrapping;
-- the verdict engine only trusts tokens with a minimum observation count.
CREATE TABLE mailbox_spam_tokens (
 tenant_id BIGINT NOT NULL,
 mailbox_id BIGINT NOT NULL,
 token TEXT NOT NULL CHECK (length(token) BETWEEN 1 AND 64),
 spam_count INTEGER NOT NULL DEFAULT 0 CHECK (spam_count BETWEEN 0 AND 1000000000),
 ham_count INTEGER NOT NULL DEFAULT 0 CHECK (ham_count BETWEEN 0 AND 1000000000),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id, mailbox_id, token),
 FOREIGN KEY (tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE mailbox_spam_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_spam_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY mailbox_owner ON mailbox_spam_tokens
 USING (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint
   AND mailbox_id = NULLIF(current_setting('urmail.mailbox_id', true), '')::bigint)
 WITH CHECK (tenant_id = NULLIF(current_setting('urmail.tenant_id', true), '')::bigint
   AND mailbox_id = NULLIF(current_setting('urmail.mailbox_id', true), '')::bigint);
GRANT SELECT, INSERT, UPDATE, DELETE ON mailbox_spam_tokens TO urmail_app, urmail_platform;
REVOKE ALL ON mailbox_spam_tokens FROM PUBLIC, urmail_dovecot;

-- The platform runtime role owns maintenance tables (restore quarantine,
-- fixture maintenance, TRUNCATE); same contract as 0028.
ALTER TABLE mailbox_spam_feedback OWNER TO urmail_platform;
ALTER TABLE mailbox_spam_senders OWNER TO urmail_platform;
ALTER TABLE mailbox_spam_tokens OWNER TO urmail_platform;
