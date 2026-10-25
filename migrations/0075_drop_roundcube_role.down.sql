-- Recreate the inert Roundcube role for controlled rollback of 0075.
-- Schema recreation remains migration 0074's responsibility.
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
  EXECUTE 'ALTER ROLE urmail_roundcube SET search_path=roundcube';
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
