-- Bind newly authenticated browser sessions to their exact HTTP origin host.
ALTER TABLE sessions ADD COLUMN origin_host TEXT NOT NULL DEFAULT '';
ALTER TABLE mailbox_sessions ADD COLUMN origin_host TEXT NOT NULL DEFAULT '';
