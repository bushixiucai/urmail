-- DATA-001: bounded, reviewed EML/MBOX import intents and per-message receipts.
CREATE TABLE mailbox_import_jobs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 request_key text NOT NULL CHECK(length(request_key) BETWEEN 8 AND 200),
 format text NOT NULL CHECK(format IN('eml','mbox')),
 filename text NOT NULL CHECK(length(filename) BETWEEN 1 AND 255),
 folder text NOT NULL CHECK(length(folder) BETWEEN 1 AND 1024),
 uid_validity bigint NOT NULL CHECK(uid_validity BETWEEN 1 AND 4294967295),
 source_digest text NOT NULL CHECK(length(source_digest)=64),
 preview_digest text NOT NULL CHECK(length(preview_digest)=64),
 duplicate_policy text NOT NULL CHECK(duplicate_policy IN('skip','keep_both')),
 source_bytes bigint NOT NULL CHECK(source_bytes BETWEEN 1 AND 20971520),
 status text NOT NULL DEFAULT 'preview' CHECK(status IN('preview','confirmed','running','partial','completed','canceled','uncertain','held')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 preview_expires_at timestamptz NOT NULL DEFAULT now()+interval '15 minutes',
 payload_expires_at timestamptz NOT NULL DEFAULT now()+interval '1 day',
 confirmed_at timestamptz,
 cancel_requested boolean NOT NULL DEFAULT false,
 lease_token text NOT NULL DEFAULT '',
 lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(tenant_id,mailbox_id,id),
 UNIQUE(tenant_id,mailbox_id,request_key),
 FOREIGN KEY(tenant_id,mailbox_id) REFERENCES mailboxes(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE mailbox_import_items (
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 job_id bigint NOT NULL,
 item_index integer NOT NULL CHECK(item_index BETWEEN 1 AND 100),
 subject text NOT NULL DEFAULT '' CHECK(length(subject)<=1000),
 message_id text NOT NULL DEFAULT '' CHECK(length(message_id)<=998),
 raw_sha256 text NOT NULL DEFAULT '' CHECK(length(raw_sha256) IN(0,64)),
 raw_bytes bigint NOT NULL DEFAULT 0 CHECK(raw_bytes BETWEEN 0 AND 20971520),
 raw_secret text NOT NULL DEFAULT '' CHECK(octet_length(raw_secret)<=16777216),
 selected boolean NOT NULL DEFAULT false,
 status text NOT NULL CHECK(status IN('ready','duplicate','invalid','pending','importing','imported','unknown','failed','skipped','canceled')),
 error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
 duplicate_of integer NOT NULL DEFAULT 0 CHECK(duplicate_of BETWEEN 0 AND 100),
 baseline_uids bigint[] NOT NULL DEFAULT '{}',
 destination_uid bigint CHECK(destination_uid BETWEEN 1 AND 4294967295),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 20),
 attempt_started_at timestamptz,
 reconciled_at timestamptz,
 reconcile_absent boolean NOT NULL DEFAULT false,
 review_reason text NOT NULL DEFAULT '' CHECK(length(review_reason)<=2000),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,mailbox_id,job_id,item_index),
 FOREIGN KEY(tenant_id,mailbox_id,job_id) REFERENCES mailbox_import_jobs(tenant_id,mailbox_id,id) ON DELETE CASCADE
);
ALTER TABLE mailbox_import_jobs OWNER TO urmail_platform;
ALTER TABLE mailbox_import_items OWNER TO urmail_platform;
ALTER TABLE mailbox_import_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_import_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE mailbox_import_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_import_items FORCE ROW LEVEL SECURITY;
CREATE POLICY import_jobs_platform ON mailbox_import_jobs TO urmail_platform USING(true) WITH CHECK(true);
CREATE POLICY import_items_platform ON mailbox_import_items TO urmail_platform USING(true) WITH CHECK(true);
CREATE POLICY import_jobs_mailbox ON mailbox_import_jobs TO urmail_app
 USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND mailbox_id=nullif(current_setting('urmail.mailbox_id',true),'')::bigint)
 WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND mailbox_id=nullif(current_setting('urmail.mailbox_id',true),'')::bigint);
CREATE POLICY import_items_mailbox ON mailbox_import_items TO urmail_app
 USING(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND mailbox_id=nullif(current_setting('urmail.mailbox_id',true),'')::bigint)
 WITH CHECK(tenant_id=nullif(current_setting('urmail.tenant_id',true),'')::bigint AND mailbox_id=nullif(current_setting('urmail.mailbox_id',true),'')::bigint);
REVOKE ALL ON mailbox_import_jobs,mailbox_import_items FROM PUBLIC,urmail_dovecot;
GRANT SELECT,INSERT,UPDATE ON mailbox_import_jobs,mailbox_import_items TO urmail_app;
GRANT USAGE,SELECT ON SEQUENCE mailbox_import_jobs_id_seq TO urmail_app;
CREATE INDEX mailbox_import_payload_expiry ON mailbox_import_jobs(tenant_id,payload_expires_at);
