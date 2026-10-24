DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='nextcloud')
 OR EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='nextcloud')
 OR EXISTS(SELECT 1 FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname='nextcloud') THEN
  RAISE EXCEPTION 'NEXTCLOUD_METADATA_REQUIRES_CONTROLLED_ROLLBACK' USING ERRCODE='23514';
 END IF;
END $$;
DROP SCHEMA nextcloud;
