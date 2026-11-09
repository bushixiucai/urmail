-- Persisted phishing_v2 verdicts: one row per analyzed message so the
-- reading pane can surface the verdict again after reopening (v0.6.15).
-- A re-analysis upserts the row; unanalyzed messages have no row and the
-- message API omits the phishing key entirely.
CREATE TABLE mailbox_ai_phishing_findings (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 folder text NOT NULL CHECK(length(folder) BETWEEN 1 AND 1024),
 uid_validity bigint NOT NULL CHECK(uid_validity BETWEEN 1 AND 4294967295),
 uid bigint NOT NULL CHECK(uid BETWEEN 1 AND 4294967295),
 input_digest text NOT NULL CHECK(input_digest ~ '^[a-f0-9]{64}$'),
 result jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(mailbox_id, folder, uid_validity, uid),
 FOREIGN KEY(tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE mailbox_ai_phishing_findings OWNER TO urmail_platform;
ALTER TABLE mailbox_ai_phishing_findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_ai_phishing_findings FORCE ROW LEVEL SECURITY;
CREATE POLICY phishing_findings_platform ON mailbox_ai_phishing_findings TO urmail_platform USING(true) WITH CHECK(true);
CREATE POLICY phishing_findings_mailbox ON mailbox_ai_phishing_findings
 USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND mailbox_id=nullif(current_setting('urmail.mailbox_id',true),'')::bigint)
 WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND mailbox_id=nullif(current_setting('urmail.mailbox_id',true),'')::bigint);
REVOKE ALL ON mailbox_ai_phishing_findings FROM PUBLIC,urmail_dovecot;
GRANT SELECT,INSERT,UPDATE ON mailbox_ai_phishing_findings TO urmail_app;
GRANT USAGE,SELECT ON SEQUENCE mailbox_ai_phishing_findings_id_seq TO urmail_app;
