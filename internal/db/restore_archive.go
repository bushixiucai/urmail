package db

import (
	"fmt"
	"strconv"
	"strings"
)

// FilterRestoreTOC preserves target bootstrap-owned infrastructure. Table and
// column ACLs remain in the archive; dropping ACLs would reopen private tables.
func FilterRestoreTOC(input string) (string, error) {
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ";") {
			continue
		}
		id, rest, ok := strings.Cut(trimmed, ";")
		if !ok {
			return "", fmt.Errorf("invalid restore object list")
		}
		if _, err := strconv.ParseUint(id, 10, 64); err != nil {
			return "", fmt.Errorf("invalid restore object identifier")
		}
		fields := strings.Fields(rest)
		if len(fields) < 3 {
			return "", fmt.Errorf("invalid restore object descriptor")
		}
		body := fields[2:]
		skip := false
		if len(body) >= 3 && body[0] == "EXTENSION" {
			if body[1] != "-" || body[2] != "pg_trgm" {
				return "", fmt.Errorf("restore extension requires explicit target provisioning")
			}
			skip = true
		}
		if len(body) >= 4 && (body[0] == "COMMENT" || body[0] == "ACL") && body[1] == "-" && body[2] == "EXTENSION" {
			if body[3] != "pg_trgm" {
				return "", fmt.Errorf("restore extension metadata is unsupported")
			}
			skip = true
		}
		if len(body) >= 3 && body[0] == "SCHEMA" && body[1] == "-" && body[2] == "public" {
			skip = true
		}
		if len(body) >= 4 && (body[0] == "COMMENT" || body[0] == "ACL") && body[1] == "-" && body[2] == "SCHEMA" && body[3] == "public" {
			skip = true
		}
		if skip {
			lines[i] = "; preserved target bootstrap: " + line
		}
	}
	return strings.Join(lines, "\n"), nil
}

// RestoreSQLPrefix starts the same transaction that receives the archive SQL.
// It removes newer application objects as well as old ones, while retaining
// superuser-owned extension members and target schema/default boundaries.
// No user-supplied text is interpolated into this maintenance program.
const RestoreSQLPrefix = `BEGIN;
SET LOCAL ROLE urmail_migrate;
DO $restore$ DECLARE r record; BEGIN
 IF EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN('r','p','v','m','S') AND c.relowner NOT IN('urmail_migrate'::regrole,'urmail_platform'::regrole) AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e')) THEN RAISE EXCEPTION 'RESTORE_TARGET_HAS_UNMANAGED_OBJECTS';END IF;
 FOR r IN SELECT c.relname,c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN('r','p','v','m','S') AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e') ORDER BY CASE WHEN c.relkind='S' THEN 1 ELSE 0 END LOOP
 EXECUTE format('DROP %s IF EXISTS public.%I CASCADE',CASE r.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW' WHEN 'S' THEN 'SEQUENCE' ELSE 'TABLE' END,r.relname);
 END LOOP;
 LOOP
 SELECT p.proname,p.prokind,pg_get_function_identity_arguments(p.oid) AS arguments INTO r FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e') LIMIT 1;
 EXIT WHEN NOT FOUND;
 IF NOT EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname=r.proname AND p.proowner IN('urmail_migrate'::regrole,'urmail_platform'::regrole)) THEN RAISE EXCEPTION 'RESTORE_TARGET_HAS_UNMANAGED_ROUTINES';END IF;
 EXECUTE format('DROP %s public.%I(%s) CASCADE',CASE r.prokind WHEN 'p' THEN 'PROCEDURE' WHEN 'a' THEN 'AGGREGATE' ELSE 'FUNCTION' END,r.proname,r.arguments);
 END LOOP;
 IF EXISTS(SELECT 1 FROM pg_namespace n WHERE (n.nspname='roundcube' AND n.nspowner NOT IN('urmail_migrate'::regrole,'urmail_roundcube'::regrole)) OR (n.nspname='nextcloud' AND n.nspowner NOT IN('urmail_migrate'::regrole,'urmail_nextcloud'::regrole))) THEN RAISE EXCEPTION 'RESTORE_TARGET_HAS_UNMANAGED_SERVICE_SCHEMA';END IF;
 DROP SCHEMA IF EXISTS roundcube CASCADE;
 DROP SCHEMA IF EXISTS nextcloud CASCADE;
END $restore$;
ALTER DEFAULT PRIVILEGES FOR ROLE urmail_migrate IN SCHEMA public REVOKE ALL ON TABLES FROM PUBLIC,urmail_app,urmail_dovecot,urmail_roundcube;
ALTER DEFAULT PRIVILEGES FOR ROLE urmail_migrate IN SCHEMA public REVOKE ALL ON SEQUENCES FROM PUBLIC,urmail_app,urmail_dovecot,urmail_roundcube;
`
