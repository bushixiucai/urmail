-- Mailbox-owned contact groups.
-- Membership must point at a live contact in the same address book.

CREATE TABLE mailbox_contact_groups (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 addressbook_id bigint NOT NULL,
 name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 100),
 created_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz,
 FOREIGN KEY(tenant_id, mailbox_id, addressbook_id) REFERENCES mailbox_addressbooks(tenant_id, mailbox_id, id),
 UNIQUE(tenant_id, mailbox_id, id)
);
CREATE UNIQUE INDEX mailbox_contact_groups_live_name_idx
 ON mailbox_contact_groups(tenant_id, mailbox_id, addressbook_id, lower(name)) WHERE deleted_at IS NULL;

CREATE TABLE mailbox_contact_group_members (
 tenant_id bigint NOT NULL,
 mailbox_id bigint NOT NULL,
 group_id bigint NOT NULL,
 contact_id bigint NOT NULL,
 PRIMARY KEY(tenant_id, mailbox_id, group_id, contact_id),
 FOREIGN KEY(tenant_id, mailbox_id, group_id) REFERENCES mailbox_contact_groups(tenant_id, mailbox_id, id),
 FOREIGN KEY(tenant_id, mailbox_id, contact_id) REFERENCES mailbox_contacts(tenant_id, mailbox_id, id)
);

CREATE FUNCTION urmail_delete_contact_group(p_tenant bigint, p_mailbox bigint, p_group bigint) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE removed bigint;
BEGIN
 UPDATE mailbox_contact_groups SET deleted_at=now()
 WHERE tenant_id=p_tenant AND mailbox_id=p_mailbox AND id=p_group AND deleted_at IS NULL;
 GET DIAGNOSTICS removed=ROW_COUNT;
 DELETE FROM mailbox_contact_group_members WHERE tenant_id=p_tenant AND mailbox_id=p_mailbox AND group_id=p_group;
 RETURN removed=1;
END $$;
ALTER FUNCTION urmail_delete_contact_group(bigint,bigint,bigint) OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_delete_contact_group(bigint,bigint,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_delete_contact_group(bigint,bigint,bigint) TO urmail_platform;
