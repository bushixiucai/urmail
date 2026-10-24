package db_test

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestRestoreTOCPreservesBootstrapAndKeepsBusinessACLs(t *testing.T) {
	input := "1; 3079 100 EXTENSION - pg_trgm\n2; 0 0 COMMENT - EXTENSION pg_trgm\n3; 2615 2200 SCHEMA - public pg_database_owner\n4; 0 0 ACL public TABLE sessions urmail_platform\n5; 0 0 ACL public COLUMN mailboxes.quota_bytes urmail_platform\n"
	out, err := db.FilterRestoreTOC(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "; preserved target bootstrap: 1;") || !strings.Contains(out, "\n4; 0 0 ACL public TABLE sessions") || !strings.Contains(out, "\n5; 0 0 ACL public COLUMN") {
		t.Fatalf("unsafe object list: %s", out)
	}
	if _, err = db.FilterRestoreTOC("1; 3079 100 EXTENSION - unknown_extension\n"); err == nil {
		t.Fatal("unknown extension silently installed")
	}
}
func TestAtomicRestoreFailureKeepsTargetFactsAndPrivateSessions(t *testing.T) {
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("native psql unavailable")
	}
	dsn := testdb.New(t)
	ctx := context.Background()
	migration, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = migration.Up(ctx); err != nil {
		t.Fatal(err)
	}
	migration.Close()
	seedRestoreExternalWork(t, dsn, true)
	// This fixture was created using the cluster test credential. Normalize only
	// its application ownership before exercising the same restricted SQL path.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, `DO $$ DECLARE r record;BEGIN FOR r IN SELECT oid::regprocedure signature FROM pg_proc WHERE pronamespace='public'::regnamespace AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_proc'::regclass AND d.objid=pg_proc.oid AND d.deptype='e') LOOP EXECUTE format('ALTER FUNCTION %s OWNER TO urmail_platform',r.signature);END LOOP;ALTER TABLE schema_migrations OWNER TO urmail_platform;END $$`); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "failed-restore.sql")
	if err = os.WriteFile(script, []byte(db.RestoreSQLPrefix+"\nSELECT 1/0;\nCOMMIT;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = nativeToolDB(t, "psql", dsn, "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--file", script).Run(); err == nil {
		t.Fatal("injected restore failure was ignored")
	}
	var intact bool
	if err = conn.QueryRow(ctx, `SELECT (SELECT count(*)=1 FROM refunds WHERE refund_no='stable-refund' AND status='pending') AND (SELECT count(*)=1 FROM roundcube.session) AND EXISTS(SELECT 1 FROM pg_extension WHERE extname='pg_trgm')`).Scan(&intact); err != nil || !intact {
		t.Fatalf("failed restore changed target facts: %v %v", intact, err)
	}
}

func TestNativeLegacySnapshotReplacesInitializedNewSchema(t *testing.T) {
	for _, tool := range []string{"pg_dump", "pg_restore", "psql"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("native database tools unavailable")
		}
	}
	source, _ := legacyDomainDatabase(t)
	seedRestoreExternalWork(t, source, false)
	target := testdb.New(t)
	migrationURL, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	params := migrationURL.Query()
	params.Set("role", "urmail_migrate")
	migrationURL.RawQuery = params.Encode()
	migrate, err := db.NewMigrator(migrationURL.String())
	if err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	migrate.Close()
	maintenance := nativeRestoreOwned(t, source, target)
	if err = db.FinalizeRestore(t.Context(), maintenance, "legacy-native-snapshot"); err != nil {
		t.Fatal(err)
	}
	migrate, err = db.NewMigrator(maintenance)
	if err != nil {
		t.Fatal(err)
	}
	defer migrate.Close()
	if err = migrate.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.HoldRestoredSideEffects(t.Context(), maintenance, "legacy-native-snapshot"); err != nil {
		t.Fatal(err)
	}
	if err = db.VerifyRuntimePrivileges(t.Context(), maintenance); err != nil {
		t.Fatal(err)
	}
	assertRestoredRefundHeld(t, target)
}
