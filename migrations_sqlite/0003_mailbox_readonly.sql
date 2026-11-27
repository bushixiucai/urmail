-- SQLite Webmail v1 read-only mailbox index tables (S3 -> S4 bridge).
-- Aligns persistent schema with internal/api/handlers/mailbox_native_sqlite_store.go
-- so inbox folder/message reads never rely on ad-hoc test-only DDL.

CREATE TABLE IF NOT EXISTS mailbox_folders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    delimiter TEXT NOT NULL DEFAULT '/',
    flags_json TEXT NOT NULL DEFAULT '[]',
    uid_validity INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)*1000000),
    UNIQUE(tenant_id, mailbox_id, name)
);

CREATE INDEX IF NOT EXISTS idx_mailbox_folders_tenant_mailbox
    ON mailbox_folders(tenant_id, mailbox_id, id);

CREATE TABLE IF NOT EXISTS mailbox_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    folder TEXT NOT NULL,
    uid_validity INTEGER NOT NULL DEFAULT 1,
    uid INTEGER NOT NULL,
    subject TEXT NOT NULL DEFAULT '',
    from_name TEXT NOT NULL DEFAULT '',
    from_email TEXT NOT NULL DEFAULT '',
    date_micros INTEGER NOT NULL DEFAULT 0,
    flags_json TEXT NOT NULL DEFAULT '[]',
    size_bytes INTEGER NOT NULL DEFAULT 0,
    has_attachments INTEGER NOT NULL DEFAULT 0,
    preview TEXT NOT NULL DEFAULT '',
    message_id TEXT NOT NULL DEFAULT '',
    in_reply_to TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)*1000000),
    UNIQUE(tenant_id, mailbox_id, folder, uid_validity, uid)
);

CREATE INDEX IF NOT EXISTS idx_mailbox_messages_folder_uid
    ON mailbox_messages(tenant_id, mailbox_id, folder, uid DESC);
