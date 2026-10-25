-- Drop the retired urmail_nextcloud cluster role when this database is clean.
-- Foreign ownership in the current database fails closed.
-- DROP ROLE is cluster-scoped: if another database still depends on the role,
-- catch dependent_objects_still_exist and leave the inert role in place.
-- REVOKE/DROP may lack ADMIN option on production migrate role — catch and NOTICE.
DO $$
DECLARE
 migrate_active boolean := (current_user = 'urmail_migrate');
BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_nextcloud') THEN
  RETURN;
 END IF;
 IF EXISTS(
  SELECT 1 FROM pg_class c
  JOIN pg_roles o ON o.oid=c.relowner
  WHERE o.rolname='urmail_nextcloud'
 ) OR EXISTS(
  SELECT 1 FROM pg_proc p
  JOIN pg_roles o ON o.oid=p.proowner
  WHERE o.rolname='urmail_nextcloud'
 ) OR EXISTS(
  SELECT 1 FROM pg_namespace n
  JOIN pg_roles o ON o.oid=n.nspowner
  WHERE o.rolname='urmail_nextcloud'
 ) OR EXISTS(
  SELECT 1 FROM pg_type t
  JOIN pg_roles o ON o.oid=t.typowner
  WHERE o.rolname='urmail_nextcloud'
 ) OR EXISTS(
  SELECT 1 FROM pg_operator op
  JOIN pg_roles o ON o.oid=op.oprowner
  WHERE o.rolname='urmail_nextcloud'
 ) OR EXISTS(
  SELECT 1 FROM pg_default_acl d
  JOIN pg_roles o ON o.oid=d.defaclrole
  WHERE o.rolname='urmail_nextcloud'
 ) THEN
  RAISE EXCEPTION 'NEXTCLOUD_ROLE_OWNS_FOREIGN_OBJECTS' USING ERRCODE='23514';
 END IF;
 IF migrate_active THEN
  PERFORM set_config('role','none',true);
 END IF;
 BEGIN
  EXECUTE 'DROP OWNED BY urmail_nextcloud';
  BEGIN
   IF pg_has_role('urmail_migrate','urmail_nextcloud','MEMBER') THEN
    EXECUTE 'REVOKE urmail_nextcloud FROM urmail_migrate';
   END IF;
  EXCEPTION WHEN insufficient_privilege THEN
   RAISE NOTICE 'urmail_nextcloud: skip REVOKE (no ADMIN option); attempting DROP ROLE';
  END;
  BEGIN
   EXECUTE 'DROP ROLE urmail_nextcloud';
  EXCEPTION
   WHEN dependent_objects_still_exist THEN
    RAISE NOTICE 'urmail_nextcloud retained: other databases still depend on the role';
   WHEN insufficient_privilege THEN
    RAISE NOTICE 'urmail_nextcloud retained: DROP ROLE requires bootstrap/superuser';
   WHEN undefined_object THEN
    NULL;
  END;
 END;
 IF migrate_active THEN
  PERFORM set_config('role','urmail_migrate',true);
 END IF;
END $$;
