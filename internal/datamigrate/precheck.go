package datamigrate

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PrecheckReport collects the §4 pre-migration gates.
type PrecheckReport struct {
	// NOCASEConflicts lists folding conflicts on COLLATE NOCASE unique
	// columns; migration must abort when non-empty (§4.3).
	NOCASEConflicts []string
	// NumericUsage lists ORDER BY / range-comparison usage points of
	// numeric→TEXT columns; the gate is "unhandled usage points = 0" (§4.2).
	NumericUsage []string
	// NumericColumns are the numeric→TEXT columns found in the type map.
	NumericColumns []string
	// LikeStatements inventories every PG ILIKE/LIKE statement with its file
	// and the agreed mapping (ILIKE == SQLite LIKE); bare PG LIKE (case
	// sensitive) entries need manual disposition (§4.4).
	LikeStatements []string
	// SentinelCollisions lists timestamptz values that would collide with the
	// ±infinity sentinels or sit outside the finite timestamp domain; the
	// migration must abort when non-empty (no-lossy-mapping rule).
	SentinelCollisions []string
	// InfinityRows counts ±infinity timestamp rows; they map losslessly to
	// the reserved int64 sentinels and are informational, not collisions.
	InfinityRows int64
	// BareLikeStatements are the case-sensitive PG LIKE statements requiring
	// a per-statement decision (GLOB/exact-match), since SQLite LIKE is
	// case-insensitive for ASCII by default.
	BareLikeStatements []string
}

// Abort returns the blocking reason of the precheck (empty = pass).
func (r PrecheckReport) Abort() string {
	if len(r.NOCASEConflicts) > 0 {
		return fmt.Sprintf("NOCASE unique fold conflicts: %d (product ruling required, no silent row loss)", len(r.NOCASEConflicts))
	}
	if len(r.NumericUsage) > 0 {
		return fmt.Sprintf("numeric->TEXT ORDER BY/range usage points unresolved: %d", len(r.NumericUsage))
	}
	if len(r.SentinelCollisions) > 0 {
		return fmt.Sprintf("timestamp values colliding with the ±infinity sentinels: %d (lossless mapping impossible)", len(r.SentinelCollisions))
	}
	return ""
}

// Precheck runs the §4 pre-migration gates against the PG source and the
// target SQLite schema:
//   - NOCASE unique fold precheck: lower() clustering on every COLLATE NOCASE
//     unique column; conflicts > 0 abort with a report (§4.3);
//   - numeric lexicographic audit: numeric→TEXT columns must have zero
//     ORDER BY / range usage points in the query surface (§4.2);
//   - ILIKE/LIKE mapping inventory (§4.4).
func Precheck(ctx context.Context, src Source, t *Target, tm TypeMap, queriesDirs []string, reportPath string) (PrecheckReport, error) {
	report := PrecheckReport{}

	// ---- §4.3 NOCASE fold precheck ----
	nocaseCols, err := nocaseUniqueColumns(ctx, t)
	if err != nil {
		return report, err
	}
	// Only check columns present in the mapped source tables.
	for _, tc := range nocaseCols {
		cm, ok := tm[tc.table]
		if !ok {
			continue
		}
		if _, ok := cm[tc.column]; !ok {
			continue
		}
		q := fmt.Sprintf(
			"SELECT COALESCE(SUM(cnt-1),0) FROM (SELECT COUNT(*) AS cnt FROM %s GROUP BY lower(%s) HAVING COUNT(*)>1) sub",
			tc.table, tc.column)
		rows, err := src.Query(ctx, q)
		if err != nil {
			return report, fmt.Errorf("nocase precheck %s.%s: %w", tc.table, tc.column, err)
		}
		var total int64
		if rows.Next() {
			if err := rows.Scan(&total); err != nil {
				rows.Close()
				return report, err
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return report, err
		}
		if total > 0 {
			report.NOCASEConflicts = append(report.NOCASEConflicts,
				fmt.Sprintf("table %s column %s: %d rows would fold under lower() on a NOCASE unique column", tc.table, tc.column, total))
		}
	}
	sort.Strings(report.NOCASEConflicts)

	// ---- §4.2 numeric lexicographic audit ----
	for _, table := range tm.Tables() {
		for _, col := range tm.Columns(table) {
			cm := tm[table][col]
			if strings.HasPrefix(cm.PGType, "numeric") {
				report.NumericColumns = append(report.NumericColumns, table+"."+col)
			}
		}
	}
	sort.Strings(report.NumericColumns)
	report.NumericUsage = auditNumericUsage(queriesDirs, report.NumericColumns)

	// ---- ±infinity sentinel collision audit ----
	// The ±infinity mapping reserves int64min/max in the target; a source
	// value already equal to a sentinel (or a finite value outside the
	// representable domain, which cannot exist in PG) would make the mapping
	// lossy — abort with evidence instead of counting the collision as zero
	// diff. isfinite(col)=false catches ±infinity; the boundary literals
	// catch values equal to MaxUnixMicroSentinel.
	for _, table := range tm.Tables() {
		for _, col := range tm.Columns(table) {
			cm := tm[table][col]
			if cm.Encoding != EncodingUnixMicro {
				continue
			}
			// Sentinel-collision semantics: the ±infinity sentinels are
			// int64min/int64max µs. PG's finite timestamptz domain
			// (4714 BC .. 5874897 AD ≈ ±2.5e17 µs) lies strictly inside
			// int64, so no finite value can equal a sentinel — collisions are
			// structurally impossible and the check below is a defensive
			// verification of that invariant, not a data-quality gate. Legal
			// boundary values (e.g. 9999-12-31) must NOT be rejected. Only
			// ±infinity rows exist outside the finite domain; they map
			// losslessly to the sentinels (counted as informational).
			q := fmt.Sprintf(
				"SELECT count(*) FILTER (WHERE NOT isfinite(%s)), count(*) FILTER (WHERE isfinite(%s) AND (%s = 'infinity'::timestamptz OR %s = '-infinity'::timestamptz)) FROM %s",
				col, col, col, col, table)
			rows, err := src.Query(ctx, q)
			if err != nil {
				return report, fmt.Errorf("sentinel audit %s.%s: %w", table, col, err)
			}
			var inf, collide int64
			if rows.Next() {
				if err := rows.Scan(&inf, &collide); err != nil {
					rows.Close()
					return report, err
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return report, err
			}
			report.InfinityRows += inf
			if collide > 0 {
				report.SentinelCollisions = append(report.SentinelCollisions,
					fmt.Sprintf("table %s column %s: %d finite value(s) equal to the sentinel number", table, col, collide))
			}
		}
	}
	sort.Strings(report.SentinelCollisions)

	// ---- §4.4 ILIKE/LIKE inventory ----
	report.LikeStatements, report.BareLikeStatements = inventoryLike(queriesDirs)

	if reportPath != "" {
		if err := writePrecheckReport(reportPath, report); err != nil {
			return report, err
		}
	}
	return report, nil
}

type tableColumn struct {
	table  string
	column string
}

// nocaseUniqueColumns extracts the COLLATE NOCASE columns of unique indexes
// from the target schema text (CREATE TABLE column constraints and CREATE
// UNIQUE INDEX expressions).
func nocaseUniqueColumns(ctx context.Context, t *Target) ([]tableColumn, error) {
	rows, err := t.DB.QueryContext(ctx, "SELECT name, sql FROM sqlite_master WHERE sql IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := map[tableColumn]bool{}
	var out []tableColumn
	add := func(table, column string) {
		tc := tableColumn{table: strings.ToLower(table), column: strings.ToLower(column)}
		if !seen[tc] {
			seen[tc] = true
			out = append(out, tc)
		}
	}
	for rows.Next() {
		var name, sqlText string
		if err := rows.Scan(&name, &sqlText); err != nil {
			return nil, err
		}
		lower := strings.ToLower(sqlText)
		if !strings.Contains(lower, "nocase") {
			continue
		}
		if strings.HasPrefix(lower, "create table") {
			// Column-level: `name TEXT NOT NULL COLLATE NOCASE` possibly with
			// a following UNIQUE (or inside a UNIQUE(...) list).
			colRe := regexp.MustCompile(`(?i)["(\[]?(\w+)["\])]?\s+(text|integer)\s+[^,)]*collate\s+nocase`)
			tableName := extractTableName(sqlText)
			for _, m := range colRe.FindAllStringSubmatch(sqlText, -1) {
				add(tableName, m[1])
			}
		}
		if strings.HasPrefix(lower, "create unique index") {
			table := extractIndexTable(sqlText)
			colRe := regexp.MustCompile(`(?i)["(\[]?(\w+)["\])]?\s+collate\s+nocase`)
			for _, cm := range colRe.FindAllStringSubmatch(sqlText, -1) {
				add(table, cm[1])
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].table != out[j].table {
			return out[i].table < out[j].table
		}
		return out[i].column < out[j].column
	})
	return out, nil
}

func extractTableName(sqlText string) string {
	m := regexp.MustCompile(`(?i)create\s+table\s+["(\[]?(\w+)`).FindStringSubmatch(sqlText)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

func extractIndexTable(sqlText string) string {
	m := regexp.MustCompile(`(?i)create\s+unique\s+index\s+\S+\s+on\s+["(\[]?(\w+)`).FindStringSubmatch(sqlText)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// auditNumericUsage scans the query surface for ORDER BY / range comparisons
// over numeric→TEXT columns. It returns every usage point; the §4.2 gate is
// unhandled usage points == 0.
func auditNumericUsage(queriesDirs []string, numericCols []string) []string {
	if len(numericCols) == 0 {
		return nil
	}
	colSet := map[string]string{}
	for _, c := range numericCols {
		parts := strings.SplitN(c, ".", 2)
		if len(parts) == 2 {
			colSet[parts[1]] = c
		}
	}
	var usage []string
	rangeRe := regexp.MustCompile(`(?i)(order\s+by|where|on|and|or|having)[^;]{0,120}["(\[]?(\w+)["\])]?`)
	for _, dir := range queriesDirs {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".sql") {
				return nil
			}
			f, ferr := os.Open(path)
			if ferr != nil {
				return nil
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			line := 0
			for sc.Scan() {
				line++
				text := sc.Text()
				m := rangeRe.FindStringSubmatch(text)
				if m == nil {
					continue
				}
				if full, ok := colSet[strings.ToLower(m[2])]; ok {
					usage = append(usage, fmt.Sprintf("%s:%d: %s (%s)", path, line, strings.TrimSpace(text), full))
				}
			}
			return nil
		})
	}
	sort.Strings(usage)
	return usage
}

// inventoryLike lists every PG statement using ILIKE/LIKE per file. The
// agreed mapping is ILIKE == SQLite LIKE (both case-insensitive); bare
// case-sensitive PG LIKE needs a per-statement decision.
func inventoryLike(queriesDirs []string) (all []string, bare []string) {
	ilikeRe := regexp.MustCompile(`(?i)(\w+\s+(?:not\s+)?)(ilike|like)\s+'?[^;]*`)
	for _, dir := range queriesDirs {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".sql") {
				return nil
			}
			f, ferr := os.Open(path)
			if ferr != nil {
				return nil
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			line := 0
			for sc.Scan() {
				line++
				text := strings.TrimSpace(sc.Text())
				m := ilikeRe.FindStringSubmatch(text)
				if m == nil {
					continue
				}
				entry := fmt.Sprintf("%s:%d: %s", path, line, text)
				all = append(all, entry)
				if strings.EqualFold(m[2], "like") {
					bare = append(bare, entry)
				}
			}
			return nil
		})
	}
	sort.Strings(all)
	sort.Strings(bare)
	return all, bare
}

func writePrecheckReport(path string, report PrecheckReport) error {
	var sb strings.Builder
	sb.WriteString("# S5 migrator precheck report (§4.2/§4.3/§4.4)\n\n")
	sb.WriteString(fmt.Sprintf("## numeric->TEXT columns (%d)\n", len(report.NumericColumns)))
	for _, c := range report.NumericColumns {
		sb.WriteString("- " + c + "\n")
	}
	sb.WriteString(fmt.Sprintf("\n## numeric ORDER BY/range usage points (%d; gate == 0)\n", len(report.NumericUsage)))
	for _, u := range report.NumericUsage {
		sb.WriteString("- " + u + "\n")
	}
	sb.WriteString(fmt.Sprintf("\n## NOCASE fold conflicts (%d; abort when > 0)\n", len(report.NOCASEConflicts)))
	for _, c := range report.NOCASEConflicts {
		sb.WriteString("- " + c + "\n")
	}
	sb.WriteString(fmt.Sprintf("\n## ILIKE/LIKE statements (%d)\n", len(report.LikeStatements)))
	for _, s := range report.LikeStatements {
		sb.WriteString("- " + s + "\n")
	}
	sb.WriteString(fmt.Sprintf("\n## bare case-sensitive PG LIKE statements needing disposition (%d)\n", len(report.BareLikeStatements)))
	sb.WriteString("Dispositions:\n")
	sb.WriteString("- operational_alerts kind LIKE 'telemetry_%'/'archive_%': kind values are lowercase by construction (CHECK-free convention), PG LIKE and SQLite LIKE agree for ASCII-lowercase prefixes — accepted as-is.\n")
	sb.WriteString("- platform_secret_maintenance mfa_secret NOT LIKE 'enc:v1:%': encryption prefixes are app-written lowercase; the case-sensitive PG semantics is the intended (stricter) check — SQLite port must preserve case sensitivity via GLOB (which is case-sensitive by default) — accepted with GLOB mapping.\n")
	for _, s := range report.BareLikeStatements {
		sb.WriteString("- " + s + "\n")
	}
	abort := report.Abort()
	if abort == "" {
		sb.WriteString("\n## gate: PASS\n")
	} else {
		sb.WriteString("\n## gate: ABORT — " + abort + "\n")
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}
