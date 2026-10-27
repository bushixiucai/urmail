-- Reject new tenant creation while restore quarantine is active.

CREATE FUNCTION urmail_restore_quarantine_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF urmail_restore_quarantine_active() THEN RAISE EXCEPTION 'restore quarantine active' USING ERRCODE='55000'; END IF;
 RETURN NEW;
END $$;
ALTER FUNCTION urmail_restore_quarantine_guard() OWNER TO urmail_platform;

CREATE TRIGGER tenant_restore_quarantine_guard BEFORE INSERT ON tenants
FOR EACH ROW EXECUTE FUNCTION urmail_restore_quarantine_guard();
