-- Retire Roundcube vendor metadata schema from the platform database.
-- Empty or missing schema is OK. Foreign ownership fails closed.
--
-- The dedicated urmail_roundcube role is dropped separately in migration 0075
-- after bootstrap/restore no longer hard-require it. Public bridge tables
-- (roundcube_bridge_requests) stay; only the isolated roundcube.* schema goes.
DO $$ BEGIN
 IF EXISTS(
  SELECT 1 FROM pg_namespace n
  JOIN pg_roles o ON o.oid=n.nspowner
  WHERE n.nspname='roundcube'
    AND o.rolname NOT IN('urmail_roundcube','urmail_migrate')
 ) THEN
  RAISE EXCEPTION 'ROUNDCUBE_SCHEMA_OWNER_REVIEW_REQUIRED' USING ERRCODE='23514';
 END IF;
END $$;

DROP SCHEMA IF EXISTS roundcube CASCADE;

-- Clear any leftover default privileges this role held in the current database.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_roundcube') THEN
  IF EXISTS(
   SELECT 1 FROM pg_class c
   JOIN pg_namespace n ON n.oid=c.relnamespace
   JOIN pg_roles o ON o.oid=c.relowner
   WHERE o.rolname='urmail_roundcube'
  ) OR EXISTS(
   SELECT 1 FROM pg_proc p
   JOIN pg_namespace n ON n.oid=p.pronamespace
   JOIN pg_roles o ON o.oid=p.proowner
   WHERE o.rolname='urmail_roundcube'
  ) OR EXISTS(
   SELECT 1 FROM pg_namespace n
   JOIN pg_roles o ON o.oid=n.nspowner
   WHERE o.rolname='urmail_roundcube'
  ) THEN
   RAISE EXCEPTION 'ROUNDCUBE_ROLE_OWNS_FOREIGN_OBJECTS' USING ERRCODE='23514';
  END IF;
  EXECUTE 'DROP OWNED BY urmail_roundcube';
 END IF;
END $$;
