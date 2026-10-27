-- Release restore quarantine only through the platform-owned function.

CREATE FUNCTION urmail_release_restore_quarantine() RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 UPDATE restore_quarantine SET active=false WHERE singleton;
 RETURN NOT urmail_restore_quarantine_active();
END $$;
ALTER FUNCTION urmail_release_restore_quarantine() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_release_restore_quarantine() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_release_restore_quarantine() TO urmail_platform;
