-- Recreate the retired Nextcloud private schema boundary for controlled rollback.
-- Does not restore any application tables; mirrors migration 0068 empty schema.
-- Role is expected to already exist from bootstrap; recreate only if missing.
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
ALTER ROLE urmail_nextcloud SET search_path=nextcloud;
DO $$ BEGIN
 IF EXISTS(
  SELECT 1 FROM pg_namespace n
  JOIN pg_roles r ON r.oid=n.nspowner
  WHERE n.nspname='nextcloud' AND r.rolname<>'urmail_nextcloud'
 ) THEN
  RAISE EXCEPTION 'NEXTCLOUD_SCHEMA_OWNER_REVIEW_REQUIRED' USING ERRCODE='23514';
 END IF;
END $$;
CREATE SCHEMA IF NOT EXISTS nextcloud AUTHORIZATION urmail_nextcloud;
REVOKE ALL ON SCHEMA nextcloud FROM PUBLIC,urmail_app,urmail_dovecot,urmail_roundcube;
ALTER DEFAULT PRIVILEGES FOR ROLE urmail_nextcloud IN SCHEMA nextcloud REVOKE ALL ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE urmail_nextcloud IN SCHEMA nextcloud REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE urmail_nextcloud IN SCHEMA nextcloud REVOKE ALL ON FUNCTIONS FROM PUBLIC;
