-- Recreate quarantine activation so callers never touch the protected table.

CREATE OR REPLACE FUNCTION urmail_begin_restore_quarantine() RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 INSERT INTO restore_quarantine(active,reason) VALUES(true,'restore')
 ON CONFLICT (singleton) DO UPDATE SET active=true, started_at=now();
 RETURN true;
END $$;

CREATE OR REPLACE FUNCTION urmail_restore_quarantine_active() RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT COALESCE((SELECT active FROM restore_quarantine), false);
$$;
