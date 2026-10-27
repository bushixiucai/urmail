-- WEBMAIL-005: per-mailbox signature, display name and preferences.
-- Mailbox principals have no SQL role of their own; access goes through
-- platform queries bound to (tenant_id, mailbox_id) taken from the verified
-- bearer session, exactly like mailbox_scheduled_sends (0076).
CREATE TABLE mailbox_settings (
 mailbox_id bigint PRIMARY KEY,
 tenant_id bigint NOT NULL,
 version integer NOT NULL DEFAULT 1 CHECK(version BETWEEN 1 AND 2147483647),
 display_name text NOT NULL DEFAULT '' CHECK(length(display_name)<=128),
 signature_text text NOT NULL DEFAULT '' CHECK(length(signature_text)<=8192),
 signature_html text NOT NULL DEFAULT '' CHECK(length(signature_html)<=16384),
 preferences jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(pg_column_size(preferences)<=8192),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,mailbox_id) REFERENCES mailboxes(tenant_id,id)
);
ALTER TABLE mailbox_settings OWNER TO urmail_platform;
ALTER TABLE mailbox_settings ENABLE ROW LEVEL SECURITY;
-- No app policies: no principal other than the platform owner reads this table.
REVOKE ALL ON mailbox_settings FROM PUBLIC,urmail_app,urmail_dovecot;
