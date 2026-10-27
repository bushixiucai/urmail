DROP TRIGGER IF EXISTS contact_group_member_scope ON mailbox_contact_group_members;
DROP FUNCTION IF EXISTS urmail_contact_group_member_guard();
DROP POLICY IF EXISTS mailbox_owner ON mailbox_contact_group_members;
DROP POLICY IF EXISTS mailbox_owner ON mailbox_contact_groups;
