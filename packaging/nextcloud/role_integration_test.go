package nextcloud_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// SET ROLE proves permissions only; a separate isolated LOGIN rehearsal remains required.
func TestNextcloudSameDatabasePrivateSchemaPermissions(t *testing.T) {
	ctx := t.Context()
	dsn := testdb.New(t)
	migrator, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrator.Close() }()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var safe bool
	if err = conn.QueryRow(ctx, `SELECT NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolinherit AND NOT rolreplication AND NOT rolbypassrls
 AND NOT EXISTS(SELECT 1 FROM pg_auth_members WHERE member=pg_roles.oid)
 AND 'search_path=nextcloud'=ANY(rolconfig) FROM pg_roles WHERE rolname='urmail_nextcloud'`).Scan(&safe); err != nil || !safe {
		t.Fatalf("unsafe Nextcloud role %v", err)
	}
	if _, err = conn.Exec(ctx, `CREATE TABLE public.nc_boundary_fixture(secret text); INSERT INTO public.nc_boundary_fixture VALUES('platform-fixture');
 CREATE TABLE roundcube.nc_boundary_fixture(secret text); INSERT INTO roundcube.nc_boundary_fixture VALUES('roundcube-fixture');
 CREATE FUNCTION public.nc_privileged_fixture() RETURNS text LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,public AS $$SELECT secret FROM public.nc_boundary_fixture$$;
 REVOKE ALL ON FUNCTION public.nc_privileged_fixture() FROM PUBLIC;`); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SET LOCAL ROLE urmail_nextcloud; SET LOCAL search_path=nextcloud"); err != nil {
		t.Fatal(err)
	}
	var owned bool
	if err = tx.QueryRow(ctx, "SELECT n.nspowner=r.oid FROM pg_namespace n CROSS JOIN pg_roles r WHERE n.nspname='nextcloud' AND r.rolname=current_user").Scan(&owned); err != nil || !owned {
		t.Fatal("Nextcloud does not own private schema")
	}
	var actualSchema string
	if err = tx.QueryRow(ctx, "SELECT current_schema()").Scan(&actualSchema); err != nil || actualSchema != "nextcloud" {
		t.Fatal("private default schema missing")
	}
	var existingObjects int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='nextcloud'").Scan(&existingObjects); err != nil || existingObjects != 0 {
		t.Fatal("conversion destination is not empty")
	}
	denied := func(sql string) {
		t.Helper()
		if _, e := tx.Exec(ctx, "SAVEPOINT boundary"); e != nil {
			t.Fatal(e)
		}
		_, e := tx.Exec(ctx, sql)
		var pe *pgconn.PgError
		if !errors.As(e, &pe) || pe.Code != "42501" {
			t.Fatalf("expected privilege denial, got %v", e)
		}
		if _, e = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT boundary; RELEASE SAVEPOINT boundary"); e != nil {
			t.Fatal(e)
		}
	}
	for _, sql := range []string{"SELECT * FROM public.nc_boundary_fixture", "INSERT INTO public.nc_boundary_fixture VALUES('forged')", "SELECT * FROM roundcube.nc_boundary_fixture", "SELECT public.nc_privileged_fixture()", "CREATE TABLE public.nc_forged(id int)", "CREATE SCHEMA nc_forged"} {
		denied(sql)
	}
	var unsafeFunctions int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname IN('public','roundcube') AND p.prosecdef AND has_function_privilege(current_user,p.oid,'EXECUTE')`).Scan(&unsafeFunctions); err != nil {
		t.Fatal(err)
	}
	if unsafeFunctions != 0 {
		t.Fatalf("Nextcloud can execute %d privileged service functions", unsafeFunctions)
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE oc_users(uid text PRIMARY KEY,displayname text);
 CREATE TABLE oc_calendars(id bigint PRIMARY KEY,principaluri text);
 CREATE TABLE oc_cards(id bigint PRIMARY KEY,addressbookid bigint,carddata text);
 CREATE TABLE oc_calendarobjects(id bigint PRIMARY KEY,calendarid bigint,calendardata text);
 CREATE TABLE oc_filecache(fileid bigint PRIMARY KEY,storage bigint,path text);
 INSERT INTO oc_users VALUES('stable-uid','Fixture');
 INSERT INTO oc_calendars VALUES(17,'principals/users/stable-uid');
 INSERT INTO oc_cards VALUES(19,23,'BEGIN:VCARD');
 INSERT INTO oc_calendarobjects VALUES(29,17,'BEGIN:VCALENDAR');
 INSERT INTO oc_filecache VALUES(31,37,'stable-uid/files/calendar.ics');`); err != nil {
		t.Fatal(err)
	}
	var exposed bool
	if err = tx.QueryRow(ctx, "SELECT has_table_privilege('urmail_app','nextcloud.oc_users','SELECT') OR has_table_privilege('urmail_roundcube','nextcloud.oc_users','SELECT') OR has_table_privilege('urmail_dovecot','nextcloud.oc_users','SELECT')").Scan(&exposed); err != nil || exposed {
		t.Fatal("private application identities exposed to another service role")
	}
	var continuity string
	if err = tx.QueryRow(ctx, `SELECT u.uid||'|'||c.principaluri||'|'||o.calendarid::text||'|'||f.fileid::text||'|'||f.storage::text FROM oc_users u CROSS JOIN oc_calendars c CROSS JOIN oc_calendarobjects o CROSS JOIN oc_filecache f`).Scan(&continuity); err != nil {
		t.Fatal(err)
	}
	if continuity != "stable-uid|principals/users/stable-uid|17|31|37" || strings.Contains(continuity, "platform-fixture") {
		t.Fatal("controlled identity/DAV/file references changed")
	}
}
