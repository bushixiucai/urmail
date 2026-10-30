-- Revert WEBMAIL-PARITY-001
DROP TABLE IF EXISTS mailbox_snippets;
DROP TABLE IF EXISTS tenant_directory_contacts;
DROP TABLE IF EXISTS tenant_directory_settings;
ALTER TABLE mailbox_contacts DROP COLUMN IF EXISTS vcard_extra;
ALTER TABLE mailbox_contacts DROP COLUMN IF EXISTS photo;
ALTER TABLE mailbox_contacts DROP COLUMN IF EXISTS birthday;
ALTER TABLE mailbox_contacts DROP COLUMN IF EXISTS addresses;
ALTER TABLE mailbox_contacts DROP COLUMN IF EXISTS job_title;
ALTER TABLE mailbox_contacts DROP COLUMN IF EXISTS organization;
