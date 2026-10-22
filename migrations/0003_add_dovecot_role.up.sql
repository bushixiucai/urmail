-- 0003_add_dovecot_role.up.sql
--
-- Dedicated database role for Dovecot authentication and userdb queries (Design review D4).
-- Dovecot authenticates users across all tenants, so it requires BYPASSRLS.
-- To follow the principle of least privilege, it is only granted SELECT on mailboxes and domains.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'urmail_dovecot') THEN
        CREATE ROLE urmail_dovecot NOLOGIN;
    END IF;
END
$$;

ALTER ROLE urmail_dovecot BYPASSRLS;

GRANT USAGE ON SCHEMA public TO urmail_dovecot;
GRANT SELECT ON mailboxes, domains TO urmail_dovecot;
