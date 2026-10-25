-- Recreate the retired Roundcube private schema boundary for controlled rollback.
-- Does not restore any application tables; mirrors migration 0042 empty schema.
-- Role is expected to already exist from bootstrap; recreate only if missing.
DO $$
DECLARE
 migrate_active boolean := (current_user = 'urmail_migrate');
BEGIN
 IF migrate_active THEN
  PERFORM set_config('role','none',true);
 END IF;
 BEGIN
  IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_roundcube') THEN
   CREATE ROLE urmail_roundcube NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  END IF;
  IF EXISTS(
   SELECT 1 FROM pg_roles
   WHERE rolname='urmail_roundcube'
     AND (rolsuper OR rolinherit OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)
  ) OR EXISTS(
   SELECT 1 FROM pg_auth_members m
   JOIN pg_roles r ON r.oid=m.member
   WHERE r.rolname='urmail_roundcube'
  ) THEN
   RAISE EXCEPTION 'ROUNDCUBE_ROLE_PRIVILEGES_UNSAFE' USING ERRCODE='23514';
  END IF;
  IF NOT pg_has_role('urmail_migrate','urmail_roundcube','MEMBER') THEN
   GRANT urmail_roundcube TO urmail_migrate;
  END IF;
 EXCEPTION WHEN insufficient_privilege THEN
  IF migrate_active THEN
   PERFORM set_config('role','urmail_migrate',true);
  END IF;
  RAISE EXCEPTION 'ROUNDCUBE_ROLE_CREATE_REQUIRES_BOOTSTRAP_LOGIN' USING ERRCODE='42501';
 END;
 IF migrate_active THEN
  PERFORM set_config('role','urmail_migrate',true);
 END IF;
END $$;
ALTER ROLE urmail_roundcube SET search_path=roundcube;
DO $$ BEGIN
 IF EXISTS(
  SELECT 1 FROM pg_namespace n
  JOIN pg_roles r ON r.oid=n.nspowner
  WHERE n.nspname='roundcube' AND r.rolname<>'urmail_roundcube'
 ) THEN
  RAISE EXCEPTION 'ROUNDCUBE_SCHEMA_OWNER_REVIEW_REQUIRED' USING ERRCODE='23514';
 END IF;
END $$;
CREATE SCHEMA IF NOT EXISTS roundcube AUTHORIZATION urmail_roundcube;
REVOKE ALL ON SCHEMA roundcube FROM PUBLIC,urmail_app,urmail_dovecot;
ALTER DEFAULT PRIVILEGES FOR ROLE urmail_roundcube IN SCHEMA roundcube REVOKE ALL ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE urmail_roundcube IN SCHEMA roundcube REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE urmail_roundcube IN SCHEMA roundcube REVOKE ALL ON FUNCTIONS FROM PUBLIC;
