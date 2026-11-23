package datamigrate

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// TableInfo describes one PG source table for the migrator.
type TableInfo struct {
	Name    string
	Columns []string          // ordered by position
	PK      []string          // primary key columns, ordered
	FKs     []ForeignKey      // outgoing foreign keys (for ordering)
	Types   map[string]string // column -> formatted pg type
}

// ForeignKey is one outgoing FK edge of a table.
type ForeignKey struct {
	Column     string
	RefTable   string
	RefColumn  string
	Constraint string
}

// IntrospectSource reads the PG catalog and builds the ordered table plan for
// every table covered by the type map that exists in the source schema.
// Tables are returned in FK-dependency order (parents first); cycles are not
// expected in this schema and fail loudly.
func IntrospectSource(ctx context.Context, src Source) ([]TableInfo, map[string]TableInfo, error) {
	rows, err := src.Query(ctx, `
SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod), a.attnum
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
WHERE n.nspname = 'public' AND c.relkind = 'r'
ORDER BY c.relname, a.attnum`)
	if err != nil {
		return nil, nil, fmt.Errorf("introspect columns: %w", err)
	}
	defer rows.Close()

	tables := map[string]TableInfo{}
	for rows.Next() {
		var t, col, typ string
		var pos int
		if err := rows.Scan(&t, &col, &typ, &pos); err != nil {
			return nil, nil, fmt.Errorf("scan column: %w", err)
		}
		ti := tables[t]
		ti.Name = t
		ti.Columns = append(ti.Columns, col)
		if ti.Types == nil {
			ti.Types = map[string]string{}
		}
		ti.Types[col] = typ
		tables[t] = ti
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	pkRows, err := src.Query(ctx, `
SELECT tc.relname, kcu.column_name, kcu.ordinal_position
FROM pg_constraint con
JOIN pg_class tc ON tc.oid = con.conrelid
JOIN pg_namespace n ON n.oid = tc.relnamespace
JOIN information_schema.key_column_usage kcu
  ON kcu.constraint_name = con.conname AND kcu.table_schema = 'public'
WHERE n.nspname = 'public' AND con.contype = 'p'
ORDER BY tc.relname, kcu.ordinal_position`)
	if err != nil {
		return nil, nil, fmt.Errorf("introspect PKs: %w", err)
	}
	defer pkRows.Close()
	for pkRows.Next() {
		var t, col string
		var pos int
		if err := pkRows.Scan(&t, &col, &pos); err != nil {
			return nil, nil, fmt.Errorf("scan pk: %w", err)
		}
		ti := tables[t]
		ti.PK = append(ti.PK, col)
		tables[t] = ti
	}
	if err := pkRows.Err(); err != nil {
		return nil, nil, err
	}

	fkRows, err := src.Query(ctx, `
SELECT tc.relname, a.attname, rc.relname, ra.attname, con.conname
FROM pg_constraint con
JOIN pg_class tc ON tc.oid = con.conrelid
JOIN pg_class rc ON rc.oid = con.confrelid
JOIN pg_namespace n ON n.oid = tc.relnamespace
JOIN unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord) ON true
JOIN unnest(con.confkey) WITH ORDINALITY AS kf(attnum, kord) ON k.ord = kord
JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
JOIN pg_attribute ra ON ra.attrelid = con.confrelid AND ra.attnum = kf.attnum
WHERE n.nspname = 'public' AND con.contype = 'f' AND tc.relname <> rc.relname
ORDER BY tc.relname, con.conname, k.ord`)
	if err != nil {
		return nil, nil, fmt.Errorf("introspect FKs: %w", err)
	}
	defer fkRows.Close()
	for fkRows.Next() {
		var t, col, rt, rc, cn string
		if err := fkRows.Scan(&t, &col, &rt, &rc, &cn); err != nil {
			return nil, nil, fmt.Errorf("scan fk: %w", err)
		}
		ti := tables[t]
		ti.FKs = append(ti.FKs, ForeignKey{Column: col, RefTable: rt, RefColumn: rc, Constraint: cn})
		tables[t] = ti
	}
	if err := fkRows.Err(); err != nil {
		return nil, nil, err
	}

	// Keep only tables covered by the type map.
	names := []string{}
	for t := range tables {
		names = append(names, t)
	}
	sort.Strings(names)
	plan := make([]TableInfo, 0, len(names))
	for _, t := range names {
		ti := tables[t]
		if len(ti.PK) == 0 {
			return nil, nil, fmt.Errorf("table %s has no primary key; keyset migration requires one", t)
		}
		plan = append(plan, ti)
	}

	ordered, err := topoSortFK(plan)
	if err != nil {
		return nil, nil, err
	}
	byName := map[string]TableInfo{}
	for _, ti := range plan {
		byName[ti.Name] = ti
	}
	return ordered, byName, nil
}

// topoSortFK orders tables so referenced tables come first ( Kahn's algorithm
// over the outgoing FK edges).
func topoSortFK(plan []TableInfo) ([]TableInfo, error) {
	byName := map[string]TableInfo{}
	for _, ti := range plan {
		byName[ti.Name] = ti
	}
	// indegree[t] = number of outgoing FK deps not yet satisfied; edges are
	// t -> referenced table (t depends on the referenced table).
	deps := map[string]map[string]bool{}
	for _, ti := range plan {
		deps[ti.Name] = map[string]bool{}
		for _, fk := range ti.FKs {
			if _, ok := byName[fk.RefTable]; ok && fk.RefTable != ti.Name {
				deps[ti.Name][fk.RefTable] = true
			}
		}
	}
	var order []string
	placed := map[string]bool{}
	for len(order) < len(plan) {
		progressed := false
		for _, ti := range plan {
			if placed[ti.Name] {
				continue
			}
			ready := true
			for dep := range deps[ti.Name] {
				if !placed[dep] {
					ready = false
					break
				}
			}
			if ready {
				placed[ti.Name] = true
				order = append(order, ti.Name)
				progressed = true
			}
		}
		if !progressed {
			var stuck []string
			for _, ti := range plan {
				if !placed[ti.Name] {
					stuck = append(stuck, ti.Name)
				}
			}
			return nil, fmt.Errorf("cyclic FK dependency among tables: %s", strings.Join(stuck, ", "))
		}
	}
	out := make([]TableInfo, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out, nil
}
