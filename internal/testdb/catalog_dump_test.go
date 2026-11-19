package testdb

// CatalogDumpTest dumps live schema facts (tables/tenant_id presence, RLS
// flags, policies, triggers, business functions, extensions, role grants)
// from one migrated fixture database. The Phase-1 isolation tooling
// (scripts/classify-tables.py, scripts/classify-port-surface.py) consumes
// this TSV as its authoritative fact source — regex parsing of migrations
// proved unreliable (dropped tables, plpgsql ENABLE loops, multiline
// policies, extension functions).
//
// Opt-in via CATALOG_DUMP_PATH; skips otherwise so normal test runs are
// unaffected. CI runs it on a clean checkout and diffs the classification
// golden (see scripts/isolation/README.md).

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestDumpCatalog(t *testing.T) {
	outPath := os.Getenv("CATALOG_DUMP_PATH")
	if outPath == "" {
		t.Skip("CATALOG_DUMP_PATH not set; catalog dump is opt-in")
	}
	dsn := New(t)
	MigrateTo(t, dsn, latestMigrationVersion(t))
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	w := func(s string) {
		if _, err := out.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}

	dump := func(section, query string) {
		rows, err := conn.Query(ctx, query)
		if err != nil {
			t.Fatal(section, err)
		}
		defer rows.Close()
		for rows.Next() {
			fds := rows.FieldDescriptions()
			cells := make([]any, len(fds))
			ptrs := make([]any, len(fds))
			for i := range ptrs {
				ptrs[i] = &cells[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(section, err)
			}
			line := section
			for _, c := range cells {
				line += "\t" + fmt.Sprint(c)
			}
			w(line + "\n")
		}
		if err := rows.Err(); err != nil {
			t.Fatal(section, err)
		}
	}

	w("TABLE\ttenant_id\trls\tforce\n")
	dump("TABLE", `
		SELECT c.relname,
		       CASE WHEN a.attname IS NULL THEN '' ELSE 'tenant_id' END,
		       c.relrowsecurity::text, c.relforcerowsecurity::text
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'tenant_id' AND a.attnum > 0 AND NOT a.attisdropped
		WHERE n.nspname = 'public' AND c.relkind IN ('r','p')
		ORDER BY c.relname`)

	w("POLICY\ttable\tname\tpermissive\tcmd\troles\n")
	dump("POLICY", `
		SELECT tablename, policyname, permissive, cmd, roles
		FROM pg_policies WHERE schemaname='public'
		ORDER BY tablename, policyname`)

	w("TRIGGER\ttable\tname\tfunction\tenabled\n")
	dump("TRIGGER", `
		SELECT c.relname, t.tgname, p.proname, t.tgenabled::text
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_proc p ON p.oid = t.tgfoid
		WHERE n.nspname='public' AND NOT t.tgisinternal
		ORDER BY c.relname, t.tgname`)

	// Business functions only: extension-owned (pg_trgm etc.) excluded —
	// the porting surface is the 119 business entries, not the 31
	// extension helpers they would otherwise inflate the count with.
	w("FUNCTION\tname\n")
	dump("FUNCTION", `
		SELECT p.proname FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname='public'
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d
		                  WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e')
		ORDER BY p.proname`)

	w("EXTENSION\tname\n")
	dump("EXTENSION", `SELECT extname FROM pg_extension ORDER BY extname`)

	w("GRANT\ttable\trole\tprivs\n")
	dump("GRANT", `
		SELECT table_name, grantee, string_agg(privilege_type, ',')
		FROM information_schema.role_table_grants
		WHERE table_schema='public' AND grantee IN ('urmail_app','urmail_dovecot','urmail_roundcube','urmail_platform')
		GROUP BY table_name, grantee ORDER BY table_name, grantee`)
}

// latestMigrationVersion reads the highest numbered migration file so the
// dump always reflects schema HEAD without a hardcoded number to maintain.
func latestMigrationVersion(t *testing.T) uint {
	t.Helper()
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	var latest uint
	for _, e := range entries {
		var n uint
		if _, err := fmt.Sscanf(e.Name(), "%d", &n); err == nil && n > latest && len(e.Name()) > 4 && e.Name()[4] == '_' {
			latest = n
		}
	}
	if latest == 0 {
		t.Fatal("no numbered migrations found")
	}
	return latest
}
