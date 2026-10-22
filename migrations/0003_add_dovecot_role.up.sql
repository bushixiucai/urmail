-- 0003_add_dovecot_role.up.sql
--
-- Dedicated database role for Dovecot authentication and userdb queries (Design review D4).
-- Dovecot authenticates users across all tenants, so it requires BYPASSRLS.
-- To follow the principle of least privilege, it is only granted SELECT on mailboxes and domains.

-- The role and its BYPASSRLS attribute are created by installer/bootstrap.sql.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'urmail_dovecot') THEN
        RAISE EXCEPTION 'urmail_dovecot is missing; run installer/bootstrap.sql first';
    END IF;
END
$$;

GRANT SELECT ON mailboxes, domains TO urmail_dovecot;
