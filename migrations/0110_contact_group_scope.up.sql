-- Keep contact groups behind the mailbox RLS boundary and require
-- the contact to live in the group's address book.

ALTER TABLE mailbox_contact_groups OWNER TO urmail_platform;
ALTER TABLE mailbox_contact_group_members OWNER TO urmail_platform;
ALTER TABLE mailbox_contact_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_contact_groups FORCE ROW LEVEL SECURITY;
ALTER TABLE mailbox_contact_group_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE mailbox_contact_group_members FORCE ROW LEVEL SECURITY;

CREATE POLICY mailbox_owner ON mailbox_contact_groups USING (
 tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint
 AND mailbox_id = nullif(current_setting('urmail.mailbox_id', true), '')::bigint
) WITH CHECK (
 tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint
 AND mailbox_id = nullif(current_setting('urmail.mailbox_id', true), '')::bigint
);
CREATE POLICY mailbox_owner ON mailbox_contact_group_members USING (
 tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint
 AND mailbox_id = nullif(current_setting('urmail.mailbox_id', true), '')::bigint
) WITH CHECK (
 tenant_id = nullif(current_setting('urmail.tenant_id', true), '')::bigint
 AND mailbox_id = nullif(current_setting('urmail.mailbox_id', true), '')::bigint
);

CREATE FUNCTION urmail_contact_group_member_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF NOT EXISTS (
  SELECT 1 FROM mailbox_contact_groups g JOIN mailbox_contacts c
   ON c.tenant_id=g.tenant_id AND c.mailbox_id=g.mailbox_id AND c.addressbook_id=g.addressbook_id
  WHERE g.tenant_id=NEW.tenant_id AND g.mailbox_id=NEW.mailbox_id AND g.id=NEW.group_id
   AND c.id=NEW.contact_id AND g.deleted_at IS NULL AND c.deleted_at IS NULL
 ) THEN RAISE EXCEPTION 'contact group scope mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_contact_group_member_guard() OWNER TO urmail_platform;
CREATE TRIGGER contact_group_member_scope BEFORE INSERT ON mailbox_contact_group_members
FOR EACH ROW EXECUTE FUNCTION urmail_contact_group_member_guard();
