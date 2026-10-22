-- Isolation constraints: push cross-tenant domain reference protection down
-- into the database (M3 review P1-1).
--
-- 1. domains gets UNIQUE (id, tenant_id) so that (tenant_id, domain_id) can be
--    referenced as a composite foreign key.
-- 2. mailboxes and mailbox_aliases reference domains(id, tenant_id), making a
--    cross-tenant domain_id an integrity error instead of a handler-layer
--    convention.

ALTER TABLE domains
    ADD CONSTRAINT domains_id_tenant_uniq UNIQUE (id, tenant_id);

ALTER TABLE mailboxes
    DROP CONSTRAINT mailboxes_domain_id_fkey,
    ADD CONSTRAINT mailboxes_domain_tenant_fk
        FOREIGN KEY (domain_id, tenant_id)
        REFERENCES domains (id, tenant_id)
        ON DELETE CASCADE;

ALTER TABLE mailbox_aliases
    DROP CONSTRAINT mailbox_aliases_domain_id_fkey,
    ADD CONSTRAINT mailbox_aliases_domain_tenant_fk
        FOREIGN KEY (domain_id, tenant_id)
        REFERENCES domains (id, tenant_id)
        ON DELETE CASCADE;

-- The NSD SOA serial is derived from the domain row's updated_at; the column
-- is maintained by trigger so every domain edit bumps the serial.
ALTER TABLE domains
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE OR REPLACE FUNCTION domains_touch_updated_at()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER domains_updated_at_touch
    BEFORE UPDATE ON domains
    FOR EACH ROW
    EXECUTE FUNCTION domains_touch_updated_at();
