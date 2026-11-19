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
	"strings"
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
			// Cells are TSV: multiline defaults/constraint definitions must
			// not break row framing — newlines and tabs collapse to spaces.
			sanitize := func(s string) string {
				r := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")
				return r.Replace(s)
			}
			line := section
			for _, c := range cells {
				line += "\t" + sanitize(fmt.Sprint(c))
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

	// Full column facts for the SQLite schema generator (S1): format_type is
	// the authoritative PG declaration; identity/serial detection feeds the
	// INTEGER PRIMARY KEY AUTOINCREMENT decision.
	w("COLUMN\ttable\tcolumn\ttype\tnotnull\tdefault\tidentity\n")
	dump("COLUMN", `
		SELECT c.relname, a.attname,
		       pg_catalog.format_type(a.atttypid, a.atttypmod),
		       a.attnotnull::text,
		       COALESCE(pg_catalog.pg_get_expr(d.adbin, d.adrelid), ''),
		       CASE WHEN a.attidentity <> '' THEN a.attidentity ELSE '' END
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		LEFT JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
		WHERE n.nspname='public' AND c.relkind IN ('r','p')
		ORDER BY c.relname, a.attnum`)

	// Constraints: PK/UNIQUE/FK/CHECK with their definitions. CHECK bodies
	// that call functions are stripped by the generator (registered for the
	// app layer) — SQLite cannot call functions from CHECK.
	w("CONSTRAINT\ttable\tname\ttype\tdefinition\n")
	dump("CONSTRAINT", `
		SELECT cl.relname, con.conname, con.contype, pg_get_constraintdef(con.oid, true)
		FROM pg_constraint con
		JOIN pg_class cl ON cl.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = cl.relnamespace
		WHERE n.nspname='public'
		ORDER BY cl.relname, con.conname`)

	// Index definitions for the index-parity gate (S1/S5): trigram GIN
	// indexes land in the dropped-index registry (no SQLite trigram).
	w("INDEXDEF\ttable\tname\tunique\tdefinition\n")
	dump("INDEXDEF", `
		SELECT t.relname, i.relname, ix.indisunique::text, pg_get_indexdef(ix.indexrelid)
		FROM pg_index ix
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_class t ON t.oid = ix.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname='public' AND NOT ix.indisprimary
		ORDER BY t.relname, i.relname`)

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
