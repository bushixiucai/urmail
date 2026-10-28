-- Per-authorized-identity presentation belongs to the existing mailbox snapshot.
ALTER TABLE mailbox_settings ADD COLUMN identity_profiles jsonb NOT NULL DEFAULT '{}'::jsonb
 CHECK(jsonb_typeof(identity_profiles)='object' AND pg_column_size(identity_profiles)<=65536);
