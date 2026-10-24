-- Groupware metadata shares the single PostgreSQL database, not mail authority.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE n.nspname='nextcloud' AND r.rolname<>'urmail_nextcloud') THEN
  RAISE EXCEPTION 'NEXTCLOUD_SCHEMA_OWNER_REVIEW_REQUIRED' USING ERRCODE='23514';
 END IF;
END $$;
CREATE SCHEMA IF NOT EXISTS nextcloud AUTHORIZATION urmail_nextcloud;
REVOKE ALL ON SCHEMA nextcloud FROM PUBLIC,urmail_app,urmail_dovecot,urmail_roundcube;
ALTER DEFAULT PRIVILEGES FOR ROLE urmail_nextcloud IN SCHEMA nextcloud REVOKE ALL ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE urmail_nextcloud IN SCHEMA nextcloud REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE urmail_nextcloud IN SCHEMA nextcloud REVOKE ALL ON FUNCTIONS FROM PUBLIC;
