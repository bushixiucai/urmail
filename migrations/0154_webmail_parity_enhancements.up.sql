-- WEBMAIL-PARITY-001: Webmail Roundcube Parity Enhancements
-- 1. Contact enhancements: organization, job title, addresses, birthday, photo, vcard_extra
ALTER TABLE mailbox_contacts ADD COLUMN IF NOT EXISTS organization text NOT NULL DEFAULT '' CHECK(length(organization) <= 200);
ALTER TABLE mailbox_contacts ADD COLUMN IF NOT EXISTS job_title text NOT NULL DEFAULT '' CHECK(length(job_title) <= 200);
ALTER TABLE mailbox_contacts ADD COLUMN IF NOT EXISTS addresses jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE mailbox_contacts ADD COLUMN IF NOT EXISTS birthday text NOT NULL DEFAULT '' CHECK(length(birthday) <= 32);
ALTER TABLE mailbox_contacts ADD COLUMN IF NOT EXISTS photo text NOT NULL DEFAULT '' CHECK(length(photo) <= 65536);
ALTER TABLE mailbox_contacts ADD COLUMN IF NOT EXISTS vcard_extra jsonb NOT NULL DEFAULT '{}'::jsonb;

-- 2. Corporate directory: tenant-wide read-only contacts published explicitly by administrator
CREATE TABLE IF NOT EXISTS tenant_directory_settings (
  tenant_id bigint PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE tenant_directory_settings OWNER TO urmail_platform;
ALTER TABLE tenant_directory_settings ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_directory_settings
  USING (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
  WITH CHECK (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint);
REVOKE ALL ON tenant_directory_settings FROM PUBLIC, urmail_app, urmail_dovecot;
GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_directory_settings TO urmail_app;

CREATE TABLE IF NOT EXISTS tenant_directory_contacts (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  display_name text NOT NULL DEFAULT '' CHECK(length(display_name) <= 200),
  given_name text NOT NULL DEFAULT '' CHECK(length(given_name) <= 100),
  family_name text NOT NULL DEFAULT '' CHECK(length(family_name) <= 100),
  organization text NOT NULL DEFAULT '' CHECK(length(organization) <= 200),
  department text NOT NULL DEFAULT '' CHECK(length(department) <= 200),
  job_title text NOT NULL DEFAULT '' CHECK(length(job_title) <= 200),
  emails jsonb NOT NULL DEFAULT '[]'::jsonb,
  phones jsonb NOT NULL DEFAULT '[]'::jsonb,
  addresses jsonb NOT NULL DEFAULT '[]'::jsonb,
  note text NOT NULL DEFAULT '' CHECK(length(note) <= 4000),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE tenant_directory_contacts OWNER TO urmail_platform;
ALTER TABLE tenant_directory_contacts ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_directory_contacts
  USING (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint)
  WITH CHECK (tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint);
REVOKE ALL ON tenant_directory_contacts FROM PUBLIC, urmail_app, urmail_dovecot;
GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_directory_contacts TO urmail_app;

-- 3. Personal canned snippets (I16)
CREATE TABLE IF NOT EXISTS mailbox_snippets (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL,
  mailbox_id bigint NOT NULL,
  title text NOT NULL CHECK(length(btrim(title)) BETWEEN 1 AND 120),
  content text NOT NULL CHECK(length(content) <= 32768),
  shortcut text NOT NULL DEFAULT '' CHECK(length(shortcut) <= 64),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id) ON DELETE CASCADE,
  UNIQUE(tenant_id, mailbox_id, id)
);
ALTER TABLE mailbox_snippets OWNER TO urmail_platform;
ALTER TABLE mailbox_snippets ENABLE ROW LEVEL SECURITY;
CREATE POLICY mailbox_owner ON mailbox_snippets
  USING (
    tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint
    AND mailbox_id = nullif(current_setting('urmail.mailbox_id', true), '')::bigint
  )
  WITH CHECK (
    tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint
    AND mailbox_id = nullif(current_setting('urmail.mailbox_id', true), '')::bigint
  );
REVOKE ALL ON mailbox_snippets FROM PUBLIC, urmail_app, urmail_dovecot;
GRANT SELECT, INSERT, UPDATE, DELETE ON mailbox_snippets TO urmail_app;
