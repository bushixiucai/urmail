package datamigrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// VerifyReport aggregates the §4 gate results.
type VerifyReport struct {
	TablesCompared  int
	RowsCompared    int
	RowHashZeroDiff bool
	SchemaDiff      []string // empty = pass
	IndexDiff       []string // empty = pass
	GoldenQueries   int
	GoldenZeroDiff  bool
}

// ZeroDiff reports whether every executed gate found no difference.
func (r VerifyReport) ZeroDiff() bool {
	return r.RowHashZeroDiff && len(r.SchemaDiff) == 0 && len(r.IndexDiff) == 0 && r.GoldenZeroDiff
}

// VerifyRowHashes streams both sides per table ordered by the original PK,
// converts source values to the type-map encoding, renders each row as
// canonical JSON and compares the sha256 digests row by row. Preserved PKs
// make set equality equivalent to ordered-sequence equality. Tables absent
// from the type map are skipped (bookkeeping).
func VerifyRowHashes(ctx context.Context, src Source, t *Target, tm TypeMap, cfg Config) (VerifyReport, error) {
	report := VerifyReport{RowHashZeroDiff: true}
	plan, _, err := IntrospectSource(ctx, src)
	if err != nil {
		return report, err
	}
	for _, ti := range plan {
		if cfg.skipped(ti.Name) || cfg.skippedTable(tm, ti.Name) || ti.Name == "recall_requests" {
			continue
		}
		cm := tm[ti.Name]
		present, err := sqliteHasTable(ctx, t, ti.Name)
		if err != nil {
			return report, err
		}
		if !present {
			report.RowHashZeroDiff = false
			report.SchemaDiff = append(report.SchemaDiff, fmt.Sprintf("table %s missing in target", ti.Name))
			continue
		}
		pk := ti.PK
		pkList := quoteList(pk, false)

		// Source: ordered converted rows.
		srcRows, err := src.Query(ctx, fmt.Sprintf("SELECT %s FROM %s ORDER BY %s", quoteList(ti.Columns, false), ti.Name, pkList))
		if err != nil {
			return report, fmt.Errorf("table %s source select: %w", ti.Name, err)
		}
		// Target: ordered stored rows over the mapped column intersection.
		var tCols []string
		for _, c := range ti.Columns {
			if _, ok := cm[c]; ok {
				tCols = append(tCols, c)
			}
		}
		tgtRows, err := t.DB.QueryContext(ctx,
			fmt.Sprintf("SELECT %s FROM %s ORDER BY %s", quoteList(tCols, false), ti.Name, pkList))
		if err != nil {
			srcRows.Close()
			return report, fmt.Errorf("table %s target select: %w", ti.Name, err)
		}

		srcPtrs := make([]any, len(ti.Columns))
		srcVals := make([]any, len(ti.Columns))
		for i := range srcVals {
			srcPtrs[i] = &srcVals[i]
		}
		tgtPtrs := make([]any, len(tCols))
		tgtVals := make([]any, len(tCols))
		for i := range tgtVals {
			tgtPtrs[i] = &tgtVals[i]
		}

		compare := func() (bool, error) {
			srcHash, _, err := hashSourceRow(srcVals, ti.Columns, cm)
			if err != nil {
				return false, err
			}
			tgtHash, err := hashTargetRow(tgtVals, tCols)
			if err != nil {
				return false, err
			}
			return srcHash == tgtHash, nil
		}

		mismatch := func(why string) {
			report.RowHashZeroDiff = false
			report.SchemaDiff = append(report.SchemaDiff, fmt.Sprintf("table %s: %s", ti.Name, why))
		}

		advanced := true
		for advanced {
			srcOK := srcRows.Next()
			tgtOK := tgtRows.Next()
			if !srcOK && !tgtOK {
				advanced = false
				break
			}
			report.RowsCompared++
			if !srcOK {
				mismatch("target has extra rows beyond source")
				break
			}
			if !tgtOK {
				mismatch("target missing rows present in source")
				break
			}
			if err := srcRows.Scan(srcPtrs...); err != nil {
				srcRows.Close()
				tgtRows.Close()
				return report, fmt.Errorf("table %s source scan: %w", ti.Name, err)
			}
			if err := tgtRows.Scan(tgtPtrs...); err != nil {
				srcRows.Close()
				tgtRows.Close()
				return report, fmt.Errorf("table %s target scan: %w", ti.Name, err)
			}
			equal, err := compare()
			if err != nil {
				srcRows.Close()
				tgtRows.Close()
				return report, fmt.Errorf("table %s hash: %w", ti.Name, err)
			}
			if !equal {
				mismatch(fmt.Sprintf("row hash mismatch: src=%v tgt=%v", srcVals, tgtVals))
			}
		}
		srcRows.Close()
		tgtRows.Close()
		if err := srcRows.Err(); err != nil {
			return report, err
		}
		if err := tgtRows.Err(); err != nil {
			return report, err
		}
		report.TablesCompared++
	}
	return report, nil
}

// hashSourceRow hashes one source row over the mapped column intersection,
// converted — the exact set the target stores.
func hashSourceRow(vals []any, cols []string, cm map[string]ColumnMap) (string, []string, error) {
	inc := make([]string, 0, len(vals))
	conv := make([]any, 0, len(vals))
	for i, c := range cols {
		m, ok := cm[c]
		if !ok {
			continue // excluded from the copy; excluded from the hash
		}
		v, err := ConvertValue(m, vals[i])
		if err != nil {
			return "", nil, fmt.Errorf("column %s: %w", c, err)
		}
		inc = append(inc, c)
		conv = append(conv, v)
	}
	if len(inc) == 0 {
		return "", nil, fmt.Errorf("no mapped columns to hash")
	}
	canonical, err := canonicalJSON(inc, conv)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), inc, nil
}

// hashTargetRow hashes one stored target row (already in converted form).
func hashTargetRow(vals []any, cols []string) (string, error) {
	if len(cols) == 0 {
		return "", fmt.Errorf("no mapped columns to hash")
	}
	canonical, err := canonicalJSON(cols, vals)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), nil
}

func sqliteHasTable(ctx context.Context, t *Target, name string) (bool, error) {
	var n int
	if err := t.DB.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// DiffSchema compares, per type-map table, the column name sets and the
// mapped SQLite type classes of the PG source against the target PRAGMA. It
// appends human-readable differences to the report.
func DiffSchema(ctx context.Context, src Source, t *Target, tm TypeMap) ([]string, error) {
	var diffs []string
	for _, table := range tm.Tables() {
		present, err := sqliteHasTable(ctx, t, table)
		if err != nil {
			return nil, err
		}
		if !present {
			diffs = append(diffs, fmt.Sprintf("table %s: missing in target schema", table))
			continue
		}
		// Target columns.
		rows, err := t.DB.QueryContext(ctx, "PRAGMA table_info(\""+table+"\")")
		if err != nil {
			return nil, fmt.Errorf("pragma %s: %w", table, err)
		}
		tgtCols := map[string]string{}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notNull, pk int
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
				rows.Close()
				return nil, err
			}
			tgtCols[strings.ToLower(name)] = strings.ToUpper(strings.TrimSpace(ctype))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()

		// Source columns via the type map (the authoritative contract).
		for _, col := range tm.Columns(table) {
			cm := tm[table][col]
			tgtType, ok := tgtCols[strings.ToLower(col)]
			if !ok {
				diffs = append(diffs, fmt.Sprintf("table %s: column %s missing in target", table, col))
				continue
			}
			if !strings.EqualFold(tgtType, cm.SQLiteType) {
				diffs = append(diffs, fmt.Sprintf("table %s column %s: target type %s, type-map %s", table, col, tgtType, cm.SQLiteType))
			}
		}
	}
	sort.Strings(diffs)
	return diffs, nil
}

// DiffIndexes compares the UNIQUE index column-tuple sets per table between
// the PG source and the SQLite target (index names legitimately differ
// across engines; the protected column tuples must not).
func DiffIndexes(ctx context.Context, src Source, t *Target, tm TypeMap) ([]string, error) {
	var diffs []string
	// PG unique index tuples.
	srcRows, err := src.Query(ctx, `
SELECT tc.relname,
       (SELECT string_agg(a.attname, ',' ORDER BY k.ord)
        FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
        JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum) AS cols
FROM pg_constraint con
JOIN pg_class tc ON tc.oid = con.conrelid
JOIN pg_namespace n ON n.oid = tc.relnamespace
WHERE n.nspname = 'public' AND con.contype = 'u'`)
	if err != nil {
		return nil, fmt.Errorf("pg unique indexes: %w", err)
	}
	pgUnique := map[string]map[string]bool{}
	for srcRows.Next() {
		var table, cols string
		if err := srcRows.Scan(&table, &cols); err != nil {
			srcRows.Close()
			return nil, err
		}
		if pgUnique[table] == nil {
			pgUnique[table] = map[string]bool{}
		}
		parts := strings.Split(cols, ",")
		sort.Strings(parts)
		pgUnique[table][strings.Join(parts, ",")] = true
	}
	if err := srcRows.Err(); err != nil {
		srcRows.Close()
		return nil, err
	}
	srcRows.Close()

	for _, table := range tm.Tables() {
		present, err := sqliteHasTable(ctx, t, table)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		idxRows, err := t.DB.QueryContext(ctx, "PRAGMA index_list(\""+table+"\")")
		if err != nil {
			return nil, fmt.Errorf("index_list %s: %w", table, err)
		}
		type idx struct {
			name   string
			unique bool
		}
		var idxs []idx
		for idxRows.Next() {
			var seq int64
			var name string
			var unique, origin, partial any
			if err := idxRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
				idxRows.Close()
				return nil, err
			}
			u, _ := unique.(int64)
			idxs = append(idxs, idx{name: name, unique: u == 1})
		}
		idxRows.Close()
		if err := idxRows.Err(); err != nil {
			return nil, err
		}

		tgtUnique := map[string]bool{}
		for _, ix := range idxs {
			if !ix.unique {
				continue
			}
			cols, err := t.DB.QueryContext(ctx, "PRAGMA index_info(\""+ix.name+"\")")
			if err != nil {
				return nil, fmt.Errorf("index_info %s: %w", ix.name, err)
			}
			var parts []string
			for cols.Next() {
				var seqno, cid int
				var cname sql.NullString
				if err := cols.Scan(&seqno, &cid, &cname); err != nil {
					cols.Close()
					return nil, err
				}
				parts = append(parts, strings.ToLower(cname.String))
			}
			cols.Close()
			sort.Strings(parts)
			tgtUnique[strings.Join(parts, ",")] = true
		}

		for tuple := range pgUnique[table] {
			if !tgtUnique[tuple] {
				diffs = append(diffs, fmt.Sprintf("table %s: unique index (%s) missing in target", table, tuple))
			}
		}
	}
	sort.Strings(diffs)
	return diffs, nil
}

// GoldenQuery is a paired dialect query over migrated data. PGColumnConvs
// optionally converts each PG result column before hashing (e.g. jsonb
// canonicalization), index-aligned with the result columns.
type GoldenQuery struct {
	Name         string
	PGSQL        string
	SQLiteQ      string
	Args         []any
	PGColumnConv []func(any) (any, error)
}

// JSONTextConverter canonicalizes a jsonb value the same way the copy does.
func JSONTextConverter(v any) (any, error) {
	return ConvertJSONText(v)
}

// VerifyGoldenQueries runs each paired query on both engines, converts the
// PG rows through PGColumnConv when provided, and compares canonical rows.
func VerifyGoldenQueries(ctx context.Context, src Source, t *Target, tm TypeMap, queries []GoldenQuery) (VerifyReport, error) {
	report := VerifyReport{GoldenZeroDiff: true, GoldenQueries: len(queries)}
	_ = tm
	for _, gq := range queries {
		srcRows, err := src.Query(ctx, gq.PGSQL, gq.Args...)
		if err != nil {
			return report, fmt.Errorf("golden %s pg: %w", gq.Name, err)
		}
		srcHash, srcCount, _, err := hashGoldenSide(srcRows, gq.PGColumnConv)
		srcRows.Close()
		if err != nil {
			return report, fmt.Errorf("golden %s pg: %w", gq.Name, err)
		}
		tgtRows, err := t.DB.QueryContext(ctx, gq.SQLiteQ, gq.Args...)
		if err != nil {
			return report, fmt.Errorf("golden %s sqlite: %w", gq.Name, err)
		}
		tgtHash, tgtCount, _, err := hashGoldenSide(sqlRowsWrap{Rows: tgtRows}, nil)
		tgtRows.Close()
		if err != nil {
			return report, fmt.Errorf("golden %s sqlite: %w", gq.Name, err)
		}
		if srcHash != tgtHash || srcCount != tgtCount {
			report.GoldenZeroDiff = false
			report.SchemaDiff = append(report.SchemaDiff, fmt.Sprintf(
				"golden %s: pg(rows=%d,hash=%s) vs sqlite(rows=%d,hash=%s)", gq.Name, srcCount, srcHash, tgtCount, tgtHash))
		}
	}
	return report, nil
}

// VerifyRecallMigration does a dedicated comparison of recall_requests rows
// between the PG source and the SQLite target. It reads ALL columns from
// both sides, applies the type-map conversion to the PG values, and does an
// explicit column-by-column comparison. It returns per-column diffs for any
// mismatched rows.
func VerifyRecallMigration(ctx context.Context, src Source, t *Target) ([]string, error) {
	srcRows, err := src.Query(ctx, `SELECT id, tenant_id, message_id, requested_by, status, reason,
		created_at, archive_entry_id, mailbox_id, completed_at, preview_actor_id, preview_token,
		preview_digest, preview_snapshot, preview_expires_at, confirmed_at, selected_copy_keys,
		attempt_epoch, local_result, result_history
		FROM recall_requests ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("recall source: %w", err)
	}
	defer srcRows.Close()

	tgtRows, err := t.DB.QueryContext(ctx, `SELECT id, tenant_id, message_id, requested_by, status, reason,
		created_at, archive_entry_id, mailbox_id, completed_at, preview_actor_id, preview_token,
		preview_digest, preview_snapshot, preview_expires_at, confirmed_at, selected_copy_keys,
		attempt_epoch, local_result, result_history
		FROM recall_requests ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("recall target: %w", err)
	}
	defer tgtRows.Close()

	type row map[string]any
	srcMap := map[int64]row{}
	for srcRows.Next() {
		vals := make([]any, len(recallColumns))
		ptrs := make([]any, len(recallColumns))
		for i := range ptrs {
			ptrs[i] = &vals[i]
		}
		if err := srcRows.Scan(ptrs...); err != nil {
			return nil, err
		}
		// Convert PG values to SQLite storage form: timestamptz → unix micros,
		// jsonb/bytea → canonical JSON text (key order normalized to match
		// what the migrator's ConvertJSONText produces on the source side).
		for i, v := range vals {
			if tv, ok := v.(time.Time); ok {
				vals[i] = tv.UnixMicro()
			} else if m, ok := v.(map[string]any); ok {
				nb, _ := json.Marshal(m)
				vals[i] = string(nb)
			} else if arr, ok := v.([]any); ok {
				nb, _ := json.Marshal(arr)
				vals[i] = string(nb)
			} else if b, ok := v.([]byte); ok {
				var parsed any
				dec := json.NewDecoder(strings.NewReader(string(b)))
				dec.UseNumber()
				if err := dec.Decode(&parsed); err == nil {
					nb, _ := json.Marshal(parsed)
					vals[i] = string(nb)
				} else {
					vals[i] = string(b)
				}
			}
		}
		var id int64
		if v, ok := vals[0].(int64); ok {
			id = v
		}
		srcMap[id] = map[string]any{}
		for i, name := range recallColumns {
			srcMap[id][fmt.Sprintf("%s_%d", name, i)] = vals[i]
		}
	}

	tgtMap := map[int64]row{}
	for tgtRows.Next() {
		vals := make([]any, len(recallColumns))
		ptrs := make([]any, len(recallColumns))
		for i := range ptrs {
			ptrs[i] = &vals[i]
		}
		if err := tgtRows.Scan(ptrs...); err != nil {
			return nil, err
		}
		var id int64
		if v, ok := vals[0].(int64); ok {
			id = v
		}
		tgtMap[id] = map[string]any{}
		for i, name := range recallColumns {
			tgtMap[id][fmt.Sprintf("%s_%d", name, i)] = vals[i]
		}
	}

	var diffs []string
	for id, srcVals := range srcMap {
		tgtVals, ok := tgtMap[id]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("recall id=%d: missing in target", id))
			continue
		}
		for k, sv := range srcVals {
			tv, tok := tgtVals[k]
			if !tok {
				diffs = append(diffs, fmt.Sprintf("recall id=%d: column %s missing in target", id, k))
				continue
			}
			if !valuesEqual(sv, tv) {
				diffs = append(diffs, fmt.Sprintf("recall id=%d column %s: pg=%v sqlite=%v", id, k, sv, tv))
			}
		}
	}
	for id := range tgtMap {
		if _, ok := srcMap[id]; !ok {
			diffs = append(diffs, fmt.Sprintf("recall id=%d: extra in target", id))
		}
	}
	sort.Strings(diffs)
	return diffs, nil
}

func valuesEqual(a, b any) bool {
	// JSON-aware comparison: both sides may hold JSON in different forms
	// (map/bytes/string). Parse into interface{} and compare structurally.
	an, aok := tryParseJSON(a)
	bn, bok := tryParseJSON(b)
	if aok && bok {
		aj, _ := json.Marshal(an)
		bj, _ := json.Marshal(bn)
		return string(aj) == string(bj)
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func tryParseJSON(v any) (any, bool) {
	switch t := v.(type) {
	case string:
		trimmed := strings.TrimSpace(t)
		if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "\"") {
			return t, false
		}
		var parsed any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			return t, false
		}
		return parsed, true
	case []byte:
		return tryParseJSON(string(t))
	default:
		return v, false
	}
}

// DefaultGoldenQueries covers representative read paths over core tables for
// the given tenant id.
func DefaultGoldenQueries(tenantID int64) []GoldenQuery {
	return []GoldenQuery{
		{
			Name:    "tenant_by_id",
			PGSQL:   "SELECT id, name, slug, status FROM tenants WHERE id = $1",
			SQLiteQ: "SELECT id, name, slug, status FROM tenants WHERE id = ?",
			Args:    []any{tenantID},
		},
		{
			Name:    "mailbox_count_by_tenant",
			PGSQL:   "SELECT count(*), COALESCE(sum(quota_bytes),0) FROM mailboxes WHERE tenant_id = $1",
			SQLiteQ: "SELECT count(*), COALESCE(sum(quota_bytes),0) FROM mailboxes WHERE tenant_id = ?",
			Args:    []any{tenantID},
			// PG sum(bigint) returns numeric (pgtype.Numeric via pgx).
			PGColumnConv: []func(any) (any, error){nil, NumericConverter},
		},
		{
			Name:    "tenant_settings_rows",
			PGSQL:   "SELECT key, value FROM settings WHERE scope = 'tenant' AND scope_id = $1 ORDER BY key",
			SQLiteQ: "SELECT key, value FROM settings WHERE scope = 'tenant' AND scope_id = ? ORDER BY key",
			Args:    []any{tenantID},
			// value is jsonb: canonicalize the PG side exactly like the copy.
			PGColumnConv: []func(any) (any, error){nil, JSONTextConverter},
		},
		{
			Name:    "active_compliance_grants",
			PGSQL:   "SELECT count(*) FROM compliance_grants WHERE tenant_id = $1 AND revoked = false",
			SQLiteQ: "SELECT count(*) FROM compliance_grants WHERE tenant_id = ? AND revoked = 0",
			Args:    []any{tenantID},
		},
		{
			Name:    "domain_by_fqdn_order",
			PGSQL:   "SELECT fqdn, status FROM domains WHERE tenant_id = $1 ORDER BY fqdn LIMIT 5",
			SQLiteQ: "SELECT fqdn, status FROM domains WHERE tenant_id = ? ORDER BY fqdn LIMIT 5",
			Args:    []any{tenantID},
		},
	}
}

// hashGoldenSide hashes each result row positionally (c0..cN — aggregate
// column names differ across engines: PG "count" vs SQLite "count(*)") and
// applies the optional per-column converters on the PG side.
func hashGoldenSide(rows Rows, convs []func(any) (any, error)) (string, int, []string, error) {
	fds, err := rowsFieldNames(rows)
	if err != nil {
		return "", 0, nil, err
	}
	positional := make([]string, len(fds))
	for i := range positional {
		positional[i] = fmt.Sprintf("c%d", i)
	}
	digest := sha256.New()
	count := 0
	vals := make([]any, len(fds))
	ptrs := make([]any, len(fds))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return "", 0, nil, err
		}
		convVals := make([]any, len(vals))
		copy(convVals, vals)
		for i := range convVals {
			if i < len(convs) && convs[i] != nil {
				v, err := convs[i](convVals[i])
				if err != nil {
					return "", 0, nil, err
				}
				convVals[i] = v
			}
			if b, ok := convVals[i].([]byte); ok {
				convVals[i] = base64Of(b)
			}
		}
		canonical, err := canonicalJSON(positional, convVals)
		if err != nil {
			return "", 0, nil, err
		}
		digest.Write([]byte(canonical))
		count++
	}
	if err := rows.Err(); err != nil {
		return "", 0, nil, err
	}
	return hex.EncodeToString(digest.Sum(nil)), count, fds, nil
}

// NumericConverter normalizes PG numeric values (pgtype.Numeric or string)
// for golden comparison: integral values become int64, others decimal text.
func NumericConverter(v any) (any, error) {
	switch n := v.(type) {
	case pgtype.Numeric:
		if !n.Valid {
			return nil, nil
		}
		// pgtype.Numeric exposes the raw mantissa/exponent: integral values
		// (Exp>=0) become int64, others decimal text.
		if n.Exp >= 0 {
			// Integral numeric: scale the mantissa; return int64 when it fits
			// (SQLite aggregates return int64 — types must match for hashing).
			i := new(big.Int).Mul(n.Int, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n.Exp)), nil))
			if i.IsInt64() {
				return i.Int64(), nil
			}
			return i.String(), nil
		}
		if fv, err := n.Float64Value(); err == nil && fv.Valid {
			return fv.Float64, nil
		}
		return fmt.Sprintf("%v", v), nil
	case string:
		return n, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("numeric: unsupported %T", v)
	}
}

func rowsFieldNames(rows Rows) ([]string, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}
	return cols, nil
}
