-- Database-side restore quarantine.
-- While active, ordinary platform writes fail before they can diverge from the snapshot.

CREATE TABLE restore_quarantine (
 singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
 active BOOLEAN NOT NULL,
 reason TEXT NOT NULL CHECK (reason IN ('restore')),
 started_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE restore_quarantine ENABLE ROW LEVEL SECURITY;
ALTER TABLE restore_quarantine FORCE ROW LEVEL SECURITY;
REVOKE ALL ON restore_quarantine FROM PUBLIC;

CREATE FUNCTION urmail_restore_quarantine_active() RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT COALESCE((SELECT active FROM restore_quarantine), false);
$$;
ALTER FUNCTION urmail_restore_quarantine_active() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_restore_quarantine_active() FROM PUBLIC;

CREATE FUNCTION urmail_begin_restore_quarantine() RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 INSERT INTO restore_quarantine(active,reason) VALUES(true,'restore')
 ON CONFLICT (singleton) DO UPDATE SET active=true, started_at=now();
 RETURN (SELECT active FROM restore_quarantine);
END $$;
ALTER FUNCTION urmail_begin_restore_quarantine() OWNER TO urmail_platform;
REVOKE ALL ON FUNCTION urmail_begin_restore_quarantine() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION urmail_begin_restore_quarantine() TO urmail_platform;

-- The platform runtime role owns maintenance tables (restore quarantine,
-- fixture maintenance, TRUNCATE); same contract as 0028.
ALTER TABLE restore_quarantine OWNER TO urmail_platform;
