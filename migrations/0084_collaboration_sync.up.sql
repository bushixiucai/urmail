-- CORE-001/002: mailbox-owned contacts and calendars.
-- Message bodies stay in Maildir/IMAP. These rows are metadata only.
-- Mailbox sessions are platform-scoped, so policies key off both
-- urmail.tenant_id and urmail.mailbox_id. Missing context matches nothing.

CREATE TABLE mailbox_addressbooks (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
 version bigint NOT NULL DEFAULT 1 CHECK(version >= 1),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz,
 FOREIGN KEY(tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id),
 UNIQUE(tenant_id, mailbox_id, id)
);
CREATE UNIQUE INDEX mailbox_addressbooks_live_name_idx
 ON mailbox_addressbooks(tenant_id, mailbox_id, lower(name)) WHERE deleted_at IS NULL;

CREATE TABLE mailbox_contacts (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 addressbook_id bigint NOT NULL,
 version bigint NOT NULL DEFAULT 1 CHECK(version >= 1),
 display_name text NOT NULL DEFAULT '' CHECK(length(display_name) <= 200),
 given_name text NOT NULL DEFAULT '' CHECK(length(given_name) <= 100),
 family_name text NOT NULL DEFAULT '' CHECK(length(family_name) <= 100),
 emails jsonb NOT NULL DEFAULT '[]'::jsonb,
 phones jsonb NOT NULL DEFAULT '[]'::jsonb,
 note text NOT NULL DEFAULT '' CHECK(length(note) <= 4000),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz,
 FOREIGN KEY(tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id),
 FOREIGN KEY(tenant_id, mailbox_id, addressbook_id) REFERENCES mailbox_addressbooks(tenant_id, mailbox_id, id),
 UNIQUE(tenant_id, mailbox_id, id)
);

CREATE TABLE mailbox_calendars (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
 timezone text NOT NULL DEFAULT 'UTC' CHECK(length(timezone) BETWEEN 1 AND 100),
 color text NOT NULL DEFAULT '' CHECK(length(color) <= 16),
 version bigint NOT NULL DEFAULT 1 CHECK(version >= 1),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz,
 FOREIGN KEY(tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id),
 UNIQUE(tenant_id, mailbox_id, id)
);
CREATE UNIQUE INDEX mailbox_calendars_live_name_idx
 ON mailbox_calendars(tenant_id, mailbox_id, lower(name)) WHERE deleted_at IS NULL;

CREATE TABLE mailbox_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 calendar_id bigint NOT NULL,
 uid text NOT NULL CHECK(length(uid) BETWEEN 1 AND 255),
 version bigint NOT NULL DEFAULT 1 CHECK(version >= 1),
 sequence integer NOT NULL DEFAULT 0 CHECK(sequence >= 0),
 summary text NOT NULL DEFAULT '' CHECK(length(summary) <= 500),
 description text NOT NULL DEFAULT '' CHECK(length(description) <= 8000),
 location text NOT NULL DEFAULT '' CHECK(length(location) <= 500),
 starts_at timestamptz NOT NULL,
 ends_at timestamptz NOT NULL,
 all_day boolean NOT NULL DEFAULT false,
 timezone text NOT NULL DEFAULT 'UTC' CHECK(length(timezone) BETWEEN 1 AND 100),
 rrule text NOT NULL DEFAULT '' CHECK(length(rrule) <= 500),
 status text NOT NULL DEFAULT 'confirmed' CHECK(status IN('confirmed','tentative','cancelled')),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz,
 FOREIGN KEY(tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id),
 FOREIGN KEY(tenant_id, mailbox_id, calendar_id) REFERENCES mailbox_calendars(tenant_id, mailbox_id, id),
 UNIQUE(tenant_id, mailbox_id, id),
 CHECK(ends_at > starts_at)
);

CREATE UNIQUE INDEX mailbox_events_live_uid_idx
 ON mailbox_events(tenant_id, mailbox_id, calendar_id, uid)
 WHERE deleted_at IS NULL;

-- One monotonic change stream per mailbox. Tombstones stay as rows with
-- deleted_at set; clients that miss the retention window must resync.
CREATE TABLE mailbox_changes (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 resource_type text NOT NULL CHECK(resource_type IN('addressbook','contact','calendar','event')),
 resource_id bigint NOT NULL CHECK(resource_id > 0),
 version bigint NOT NULL CHECK(version >= 1),
 deleted boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id)
);

CREATE INDEX mailbox_changes_cursor_idx
 ON mailbox_changes(tenant_id, mailbox_id, id);

CREATE TABLE mailbox_sync_cursors (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 protocol text NOT NULL CHECK(protocol IN('web','carddav','caldav','eas')),
 client_key text NOT NULL CHECK(length(client_key) BETWEEN 1 AND 200),
 cursor_id bigint NOT NULL DEFAULT 0 CHECK(cursor_id >= 0),
 epoch bigint NOT NULL DEFAULT 1 CHECK(epoch >= 1),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id),
 UNIQUE(tenant_id, mailbox_id, protocol, client_key)
);

-- Durable work that is not itself a mail body. Callers supply idempotency_key.
CREATE TABLE mailbox_jobs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 kind text NOT NULL CHECK(length(kind) BETWEEN 1 AND 64),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 200),
 payload jsonb NOT NULL DEFAULT '{}'::jsonb,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN('pending','leased','done','failed','uncertain')),
 claim_token bigint NOT NULL DEFAULT 0,
 lease_until timestamptz,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts >= 0),
 last_error text,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id, mailbox_id) REFERENCES mailboxes(tenant_id, id),
 UNIQUE(tenant_id, mailbox_id, kind, idempotency_key)
);

CREATE INDEX mailbox_jobs_due_idx
 ON mailbox_jobs(status, lease_until, id)
 WHERE status IN('pending','leased');

ALTER TABLE mailbox_addressbooks OWNER TO urmail_platform;
ALTER TABLE mailbox_contacts OWNER TO urmail_platform;
ALTER TABLE mailbox_calendars OWNER TO urmail_platform;
ALTER TABLE mailbox_events OWNER TO urmail_platform;
ALTER TABLE mailbox_changes OWNER TO urmail_platform;
ALTER TABLE mailbox_sync_cursors OWNER TO urmail_platform;
ALTER TABLE mailbox_jobs OWNER TO urmail_platform;

ALTER TABLE mailbox_addressbooks ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_contacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_calendars ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_changes ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_sync_cursors ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_jobs ENABLE ROW LEVEL SECURITY;

-- Fail closed: a tenant context without the mailbox, or neither, sees no rows.
-- A mailbox context cannot read another mailbox in the same tenant.
DO $$
DECLARE target text;
BEGIN
 FOREACH target IN ARRAY ARRAY[
  'mailbox_addressbooks','mailbox_contacts','mailbox_calendars','mailbox_events',
  'mailbox_changes','mailbox_sync_cursors','mailbox_jobs']
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

REVOKE ALL ON mailbox_addressbooks, mailbox_contacts, mailbox_calendars, mailbox_events,
 mailbox_changes, mailbox_sync_cursors, mailbox_jobs
 FROM PUBLIC, urmail_app, urmail_dovecot;
GRANT SELECT, INSERT, UPDATE, DELETE ON mailbox_addressbooks, mailbox_contacts, mailbox_calendars,
 mailbox_events, mailbox_changes, mailbox_sync_cursors, mailbox_jobs TO urmail_app;
