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
    -- urmail_roundcube and urmail_nextcloud are retired: do not create them for
    -- new installs. Historical migrations 0042/0068 still AUTHORIZE them during
    -- migrate Up; the migrator ensures each role ephemerally until 0075/0073.
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'urmail_dovecot') THEN
        CREATE ROLE urmail_dovecot NOLOGIN;
    END IF;
END
$$;

-- The platform role is the administrative path: it owns the tables and must
-- see across tenants. The Dovecot role needs the same visibility for userdb
-- queries but only receives SELECT grants in migration 0003.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_platform' AND NOT rolbypassrls) THEN ALTER ROLE urmail_platform BYPASSRLS; END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_dovecot' AND NOT rolbypassrls) THEN ALTER ROLE urmail_dovecot BYPASSRLS; END IF;
END $$;

-- The migration role creates the schema objects. PostgreSQL 15+ no longer
-- grants CREATE on public to PUBLIC, so this must be explicit even when the
-- role owns the database. WITH GRANT OPTION lets the migration role grant
-- schema usage to the runtime roles.
ALTER SCHEMA public OWNER TO urmail_platform;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO urmail_migrate WITH GRANT OPTION;
GRANT USAGE, CREATE ON SCHEMA public TO urmail_platform;
GRANT USAGE ON SCHEMA public TO urmail_app;
GRANT USAGE ON SCHEMA public TO urmail_dovecot;

-- 0001 transfers table ownership to urmail_platform. The migration role must
-- be a member of the owning role to perform that ALTER TABLE ... OWNER TO.
DO $$ BEGIN IF NOT pg_has_role('urmail_migrate','urmail_platform','MEMBER') THEN GRANT urmail_platform TO urmail_migrate; END IF; END $$;

-- pg_trgm is an untrusted extension: only a superuser can create it, and the
-- migration role is deliberately NOSUPERUSER. Compliance substring search
-- (migration 0028) degrades to the slower tsvector path when it is missing,
-- so the installer creates it here, in the superuser stage, BEFORE migrations
-- run. Skip silently when the host already provides it.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
        CREATE EXTENSION IF NOT EXISTS pg_trgm;
    END IF;
EXCEPTION
    WHEN OTHERS THEN
        RAISE WARNING 'could not install pg_trgm (%); compliance substring search will be slower', SQLERRM;
END
$$;

-- The migration role creates isolated service schemas inside this database.
DO $$ BEGIN EXECUTE format('GRANT CREATE ON DATABASE %I TO urmail_migrate',current_database()); END $$;

-- Legacy clusters may still carry retired service roles until 0073/0075.
-- Harden only when present; never recreate them here. Older bootstrap created
-- urmail_roundcube with default INHERIT — strip unsafe attributes in place.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_roundcube') THEN
  ALTER ROLE urmail_roundcube NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  IF EXISTS(SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member WHERE r.rolname='urmail_roundcube') THEN
   RAISE EXCEPTION 'ROUNDCUBE_ROLE_PRIVILEGES_UNSAFE';
  END IF;
  IF NOT pg_has_role('urmail_migrate','urmail_roundcube','MEMBER') THEN GRANT urmail_roundcube TO urmail_migrate; END IF;
  EXECUTE 'ALTER ROLE urmail_roundcube SET search_path=roundcube';
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_nextcloud') THEN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_nextcloud' AND (rolsuper OR rolinherit OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls))
  OR EXISTS(SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member WHERE r.rolname='urmail_nextcloud')
  THEN RAISE EXCEPTION 'NEXTCLOUD_ROLE_PRIVILEGES_UNSAFE'; END IF;
  IF NOT pg_has_role('urmail_migrate','urmail_nextcloud','MEMBER') THEN GRANT urmail_nextcloud TO urmail_migrate; END IF;
  EXECUTE 'ALTER ROLE urmail_nextcloud SET search_path=nextcloud';
 END IF;
END $$;
