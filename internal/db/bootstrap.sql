-- Cluster-level bootstrap for urMail.cn.
--
-- This file is executed by the installer as a PostgreSQL superuser, before
-- the golang-migrate migrations run. It creates the cluster roles once and
-- gives the migration role membership in the platform role so that 0001 can
-- transfer table ownership to urmail_platform.
--
-- The application never runs this file. Runtime roles are created NOLOGIN
-- here and receive LOGIN/passwords from the installer.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'urmail_migrate') THEN
        CREATE ROLE urmail_migrate NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'urmail_app') THEN
        CREATE ROLE urmail_app NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'urmail_platform') THEN
        CREATE ROLE urmail_platform NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'urmail_dovecot') THEN
        CREATE ROLE urmail_dovecot NOLOGIN;
    END IF;
END
$$;

-- The platform role is the administrative path: it owns the tables and must
-- see across tenants. The Dovecot role needs the same visibility for userdb
-- queries but only receives SELECT grants in migration 0003.
ALTER ROLE urmail_platform BYPASSRLS;
ALTER ROLE urmail_dovecot BYPASSRLS;

-- The migration role creates the schema objects. PostgreSQL 15+ no longer
-- grants CREATE on public to PUBLIC, so this must be explicit even when the
-- role owns the database. WITH GRANT OPTION lets the migration role grant
-- schema usage to the runtime roles.
GRANT USAGE, CREATE ON SCHEMA public TO urmail_migrate WITH GRANT OPTION;
GRANT USAGE, CREATE ON SCHEMA public TO urmail_platform;
GRANT USAGE ON SCHEMA public TO urmail_app;
GRANT USAGE ON SCHEMA public TO urmail_dovecot;

-- 0001 transfers table ownership to urmail_platform. The migration role must
-- be a member of the owning role to perform that ALTER TABLE ... OWNER TO.
GRANT urmail_platform TO urmail_migrate;
