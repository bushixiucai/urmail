-- EAS transport device state (Task EAS-002).
-- Stores per-device negotiated protocol state and the FolderSync folder
-- snapshot used to diff hierarchy changes. Every row belongs to exactly one
-- mailbox and is protected by the same mailbox RLS pattern as migration 0084.

CREATE TABLE mailbox_eas_devices (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 device_id text NOT NULL CHECK(length(device_id) BETWEEN 1 AND 200),
 protocol_version text NOT NULL CHECK(protocol_version IN('12.1','14.0','14.1','16.0','16.1')),
 user_agent text NOT NULL DEFAULT '' CHECK(length(user_agent) <= 200),
 policy_key bigint NOT NULL DEFAULT 0 CHECK(policy_key >= 0),
 provisioned boolean NOT NULL DEFAULT false,
 last_seen_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id),
 UNIQUE(tenant_id, mailbox_id, device_id)
);

-- Per-device snapshot of the FolderSync hierarchy so Adds/Deletes/Updates can
-- be computed without trusting client state.
CREATE TABLE mailbox_eas_folder_state (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 device_id text NOT NULL CHECK(length(device_id) BETWEEN 1 AND 200),
 folder_id text NOT NULL CHECK(length(folder_id) BETWEEN 1 AND 300),
 parent_id text NOT NULL DEFAULT '' CHECK(length(parent_id) <= 300),
 display_name text NOT NULL DEFAULT '' CHECK(length(display_name) <= 200),
 folder_type integer NOT NULL CHECK(folder_type BETWEEN 1 AND 20),
 folder_class text NOT NULL DEFAULT '' CHECK(folder_class IN('','Email','Calendar','Contacts')),
 change_seq bigint NOT NULL,
 FOREIGN KEY(tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id),
 UNIQUE(tenant_id, mailbox_id, device_id, folder_id)
);

ALTER TABLE mailbox_eas_devices OWNER TO urmail_platform;
ALTER TABLE mailbox_eas_folder_state OWNER TO urmail_platform;

ALTER TABLE mailbox_eas_devices ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_eas_devices FORCE ROW LEVEL SECURITY;
ALTER TABLE mailbox_eas_folder_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_eas_folder_state FORCE ROW LEVEL SECURITY;

-- Fail closed: identical policy to migration 0084 collaboration tables.
DO $$
DECLARE target text;
BEGIN
 FOREACH target IN ARRAY ARRAY['mailbox_eas_devices','mailbox_eas_folder_state']
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

REVOKE ALL ON mailbox_eas_devices, mailbox_eas_folder_state
 FROM PUBLIC, urmail_app, urmail_dovecot;
GRANT SELECT, INSERT, UPDATE, DELETE ON mailbox_eas_devices, mailbox_eas_folder_state TO urmail_app;