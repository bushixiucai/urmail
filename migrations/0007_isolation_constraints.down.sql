-- Reverse of 0007_isolation_constraints.up.sql.

ALTER TABLE mailboxes
    DROP CONSTRAINT mailboxes_domain_tenant_fk,
    ADD CONSTRAINT mailboxes_domain_id_fkey
        FOREIGN KEY (domain_id)
        REFERENCES domains (id)
        ON DELETE CASCADE;

ALTER TABLE mailbox_aliases
    DROP CONSTRAINT mailbox_aliases_domain_tenant_fk,
    ADD CONSTRAINT mailbox_aliases_domain_id_fkey
        FOREIGN KEY (domain_id)
        REFERENCES domains (id)
        ON DELETE CASCADE;

ALTER TABLE domains
    DROP CONSTRAINT domains_id_tenant_uniq;

DROP TRIGGER IF EXISTS domains_updated_at_touch ON domains;
DROP FUNCTION IF EXISTS domains_touch_updated_at();

ALTER TABLE domains
    DROP COLUMN updated_at;
