-- Recreate the inert Nextcloud role for controlled rollback of 0073.
-- Schema recreation remains migration 0072's responsibility.
DO $$
DECLARE
 migrate_active boolean := (current_user = 'urmail_migrate');
BEGIN
 IF migrate_active THEN
  PERFORM set_config('role','none',true);
 END IF;
 BEGIN
  IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_nextcloud') THEN
   CREATE ROLE urmail_nextcloud NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  END IF;
  IF EXISTS(
   SELECT 1 FROM pg_roles
   WHERE rolname='urmail_nextcloud'
     AND (rolsuper OR rolinherit OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)
  ) OR EXISTS(
   SELECT 1 FROM pg_auth_members m
   JOIN pg_roles r ON r.oid=m.member
   WHERE r.rolname='urmail_nextcloud'
  ) THEN
   RAISE EXCEPTION 'NEXTCLOUD_ROLE_PRIVILEGES_UNSAFE' USING ERRCODE='23514';
  END IF;
  IF NOT pg_has_role('urmail_migrate','urmail_nextcloud','MEMBER') THEN
   GRANT urmail_nextcloud TO urmail_migrate;
  END IF;
  EXECUTE 'ALTER ROLE urmail_nextcloud SET search_path=nextcloud';
 EXCEPTION WHEN insufficient_privilege THEN
  IF migrate_active THEN
   PERFORM set_config('role','urmail_migrate',true);
  END IF;
  RAISE EXCEPTION 'NEXTCLOUD_ROLE_CREATE_REQUIRES_BOOTSTRAP_LOGIN' USING ERRCODE='42501';
 END;
 IF migrate_active THEN
  PERFORM set_config('role','urmail_migrate',true);
 END IF;
END $$;
