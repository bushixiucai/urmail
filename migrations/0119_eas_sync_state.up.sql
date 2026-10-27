
-- EAS-003 data sync state (extends the EAS-002 device tables from 0118).
-- Per-device, per-collection sync keys and the per-item snapshot used to
-- compute Adds/Changes/Deletes without trusting client state. Every row
-- belongs to exactly one mailbox and follows the 0084/0118 RLS pattern.

CREATE TABLE mailbox_eas_collection_state (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 device_id text NOT NULL CHECK(length(device_id) BETWEEN 1 AND 200),
 folder_id text NOT NULL CHECK(length(folder_id) BETWEEN 1 AND 300),
 sync_epoch bigint NOT NULL DEFAULT 1 CHECK(sync_epoch >= 1),
 change_seq bigint NOT NULL DEFAULT 0 CHECK(change_seq >= 0),
 uid_validity bigint NOT NULL DEFAULT 0 CHECK(uid_validity >= 0),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id),
 UNIQUE(tenant_id, mailbox_id, device_id, folder_id)
);

-- One row per item the device has already been shown. Tombstoned by removal,
-- never resurrected: a deleted row only reappears through a new change-log
-- entry or a fresh full sync (sync key 0).
CREATE TABLE mailbox_eas_item_state (
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 device_id text NOT NULL CHECK(length(device_id) BETWEEN 1 AND 200),
 folder_id text NOT NULL CHECK(length(folder_id) BETWEEN 1 AND 300),
 item_id text NOT NULL CHECK(length(item_id) BETWEEN 1 AND 300),
 fingerprint text NOT NULL DEFAULT '' CHECK(length(fingerprint) <= 300),
 PRIMARY KEY(tenant_id, mailbox_id, device_id, folder_id, item_id)
);

ALTER TABLE mailbox_eas_collection_state OWNER TO urmail_platform;
ALTER TABLE mailbox_eas_item_state OWNER TO urmail_platform;

ALTER TABLE mailbox_eas_collection_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_eas_collection_state FORCE ROW LEVEL SECURITY;
ALTER TABLE mailbox_eas_item_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_eas_item_state FORCE ROW LEVEL SECURITY;

-- Fail closed: identical policy to migrations 0084 and 0118.
DO $$
DECLARE target text;
BEGIN
 FOREACH target IN ARRAY ARRAY['mailbox_eas_collection_state','mailbox_eas_item_state']
 LOOP
  EXECUTE format(
   'CREATE POLICY mailbox_owner ON %I USING (
      tenant_id = nullif(current_setting(''urmail.tenant_id'', true), '''')::bigint
      AND mailbox_id = nullif(current_setting(''urmail.mailbox_id'', true), '''')::bigint
    ) WITH CHECK (
      tenant_id = nullif(current_setting(''urmail.tenant_id'', true), '''')::bigint
      AND mailbox_id = nullif(current_setting(''urmail.mailbox_id'', true), '''')::bigint
    )', target);
 END LOOP;
END $$;

REVOKE ALL ON mailbox_eas_collection_state, mailbox_eas_item_state
 FROM PUBLIC, urmail_app, urmail_dovecot;
GRANT SELECT, INSERT, UPDATE, DELETE ON mailbox_eas_collection_state, mailbox_eas_item_state TO urmail_app;
