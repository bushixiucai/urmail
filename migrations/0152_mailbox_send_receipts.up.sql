-- Durable immediate-send intentions. Recovery material is encrypted by the application.
CREATE TABLE mailbox_send_receipts (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 request_key text NOT NULL CHECK(length(request_key) BETWEEN 16 AND 128),
 request_hash text NOT NULL CHECK(length(request_hash)=64),
 message_id text NOT NULL CHECK(length(message_id) BETWEEN 3 AND 998),
 status text NOT NULL DEFAULT 'prepared' CHECK(status IN('prepared','submitting','accepted','rejected','uncertain')),
 sent_copy_state text NOT NULL DEFAULT 'none' CHECK(sent_copy_state IN('none','pending','unknown','complete','held')),
 raw_secret text NOT NULL CHECK(octet_length(raw_secret)<=33554432),
 smtp_secret text NOT NULL DEFAULT '',
 smtp_password_version text NOT NULL DEFAULT '',
 draft_folder text NOT NULL DEFAULT '',
 draft_uid bigint NOT NULL DEFAULT 0 CHECK(draft_uid BETWEEN 0 AND 4294967295),
 draft_uidvalidity bigint NOT NULL DEFAULT 0 CHECK(draft_uidvalidity BETWEEN 0 AND 4294967295),
 draft_released boolean NOT NULL DEFAULT false,
 claim_token bigint NOT NULL DEFAULT 0,
 lease_until timestamptz,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 copy_attempts integer NOT NULL DEFAULT 0,
 copy_pending_since timestamptz,
 accepted_at timestamptz,
 last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(tenant_id,mailbox_id,request_key),
 FOREIGN KEY(tenant_id,mailbox_id) REFERENCES mailboxes(tenant_id,id) ON DELETE CASCADE
);
ALTER TABLE mailbox_send_receipts OWNER TO urmail_platform;
ALTER TABLE mailbox_send_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_send_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY send_receipts_platform ON mailbox_send_receipts TO urmail_platform USING(true) WITH CHECK(true);
CREATE POLICY send_receipts_mailbox ON mailbox_send_receipts TO urmail_app
 USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND mailbox_id=nullif(current_setting('urmail.mailbox_id',true),'')::bigint)
 WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND mailbox_id=nullif(current_setting('urmail.mailbox_id',true),'')::bigint);
REVOKE ALL ON mailbox_send_receipts FROM PUBLIC,urmail_dovecot,urmail_app;
REVOKE ALL ON SEQUENCE mailbox_send_receipts_id_seq FROM PUBLIC,urmail_dovecot,urmail_app;
GRANT SELECT ON mailbox_send_receipts TO urmail_app;
CREATE UNIQUE INDEX send_receipt_active_draft ON mailbox_send_receipts(tenant_id,mailbox_id,draft_folder,draft_uidvalidity,draft_uid)
 WHERE draft_uid>0 AND NOT draft_released AND status IN('prepared','submitting','accepted','uncertain');
CREATE INDEX send_receipt_copy_due ON mailbox_send_receipts(next_attempt_at,id) WHERE status='accepted' AND sent_copy_state IN('pending','unknown');
