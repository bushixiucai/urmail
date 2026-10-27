-- Platform tests and platform-owned functions maintain contact groups.
-- Application roles receive the same scoped DML used by contacts.

GRANT SELECT, INSERT, UPDATE, DELETE ON mailbox_contact_groups, mailbox_contact_group_members TO urmail_platform, urmail_app;
