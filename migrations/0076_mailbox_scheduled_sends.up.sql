-- T2: schedule metadata in PG; message bodies stay in IMAP Drafts only.
-- smtp_secret holds enc:v1 ciphertext of the SMTP/IMAP password sealed at
-- schedule-create time (worker decrypts at fire time). Platform-only access.
CREATE TABLE mailbox_scheduled_sends (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 draft_folder text NOT NULL CHECK(length(draft_folder) BETWEEN 1 AND 1024),
 draft_uid bigint NOT NULL CHECK(draft_uid BETWEEN 1 AND 4294967295),
 send_at timestamptz NOT NULL,
 status text NOT NULL DEFAULT 'pending'
  CHECK(status IN('pending','sending','sent','failed','cancelled')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts >= 0),
 last_error text,
 smtp_secret text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,mailbox_id) REFERENCES mailboxes(tenant_id,id)
);
ALTER TABLE mailbox_scheduled_sends OWNER TO urmail_platform;
ALTER TABLE mailbox_scheduled_sends ENABLE ROW LEVEL SECURITY;
-- No app policies/grants: mailbox principals schedule via platform queries.
-- urmail_roundcube is retired (0075); do not reference it here.
REVOKE ALL ON mailbox_scheduled_sends FROM PUBLIC,urmail_app,urmail_dovecot;
CREATE INDEX mailbox_scheduled_sends_due_idx
 ON mailbox_scheduled_sends(send_at,id)
 WHERE status='pending';
CREATE INDEX mailbox_scheduled_sends_mailbox_idx
 ON mailbox_scheduled_sends(tenant_id,mailbox_id,status,send_at);
