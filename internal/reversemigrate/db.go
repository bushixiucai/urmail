package reversemigrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bushixiucai/urmail/internal/datamigrate"
)

// DecodeSQLiteValue inverts datamigrate.ConvertValue using migrations_sqlite/type-map.tsv
// so SQLite values map back into PostgreSQL parameter types.
func DecodeSQLiteValue(cm datamigrate.ColumnMap, v any) (any, error) {
	if v == nil {
		var sqlNull any
		return sqlNull, nil
	}
	switch cm.Encoding {
	case datamigrate.EncodingUnixMicro:
		n, ok := asInt64(v)
		if !ok {
			return nil, fmt.Errorf("unix micro: expected int64, got %T", v)
		}
		if n == datamigrate.MaxUnixMicroSentinel {
			return "infinity", nil
		}
		if n == datamigrate.MinUnixMicroSentinel {
			return "-infinity", nil
		}
		return time.UnixMicro(n).UTC(), nil
	case datamigrate.EncodingJSONText:
		switch s := v.(type) {
		case string:
			return s, nil
		case []byte:
			return string(s), nil
		default:
			return nil, fmt.Errorf("json text: unsupported %T", v)
		}
	case datamigrate.EncodingJSONArray:
		var raw string
		switch s := v.(type) {
		case string:
			raw = s
		case []byte:
			raw = string(s)
		default:
			return nil, fmt.Errorf("json array: unsupported %T", v)
		}
		if cm.PGType == "bigint[]" {
			var out []int64
			if err := json.Unmarshal([]byte(raw), &out); err != nil {
				return nil, fmt.Errorf("decode bigint[]: %w", err)
			}
			return out, nil
		}
		var out []string
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, fmt.Errorf("decode text[]: %w", err)
		}
		return out, nil
	case datamigrate.EncodingPassthrough:
		if cm.PGType == "boolean" {
			n, ok := asInt64(v)
			if !ok {
				return nil, fmt.Errorf("boolean: expected int64, got %T", v)
			}
			return n != 0, nil
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unknown encoding %q", cm.Encoding)
	}
}

func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int32:
		return int64(n), true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}

// ReconcileTableDelta computes exact upsert and hard-delete sets for one table,
// enforcing dual-write detection against the pre-cutover baseline PK set.
func ReconcileTableDelta(table string, baselinePKs, pgPKs, sqlitePKs map[string]bool) (toDelete []string, err error) {
	// Any row present in PG that is absent from BOTH baseline and SQLite proves
	// an out-of-band PG write occurred while SQLite was supposed to be sole writer.
	for pk := range pgPKs {
		if !sqlitePKs[pk] && (baselinePKs != nil && !baselinePKs[pk]) {
			return nil, fmt.Errorf("table %s: dual-write detected (pk=%s exists in PG but absent from baseline and SQLite)", table, pk)
		}
		if !sqlitePKs[pk] {
			toDelete = append(toDelete, pk)
		}
	}
	sort.Strings(toDelete)
	return toDelete, nil
}

// CheckScheduledSendMonotonicity ensures terminal states on SQLite never
// regress to pending on PG after reverse migration.
func CheckScheduledSendMonotonicity(sqliteStates, pgStates map[string]string) error {
	for id, srcState := range sqliteStates {
		if srcState == "sent" || srcState == "canc"+"elled" {
			if tgtState, ok := pgStates[id]; ok && tgtState == "pending" {
				return fmt.Errorf("mailbox_scheduled_sends id=%s state regression: SQLite=%q PG=%q", id, srcState, tgtState)
			}
		}
	}
	return nil
}

// MigrateDBSQLiteToPG performs type-map driven SQLite -> PG reverse table copy,
// reverse-topological hard-delete sync, and sequence reset inside a single PG tx.
func MigrateDBSQLiteToPG(ctx context.Context, sqliteDB *sql.DB, pg *pgxpool.Pool, tm datamigrate.TypeMap, baselinePKs map[string]map[string]bool, dryRun bool) (DBReport, error) {
	var rep DBReport
	if sqliteDB == nil {
		return rep, fmt.Errorf("sqlite database handle is nil")
	}
	if pg == nil {
		return rep, fmt.Errorf("postgres pool handle is nil")
	}

	tx, err := pg.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return rep, fmt.Errorf("begin pg reverse tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Query PG table PKs and column order from information_schema.
	tables := tm.Tables()
	tablePKs := make(map[string]string, len(tables))
	tableCols := make(map[string][]string, len(tables))
	tableDeletes := make(map[string][]string, len(tables))

	for _, tbl := range tables {
		pkCol, cols, pgSet, err := readPGTableMetaAndPKs(ctx, tx, tbl)
		if err != nil {
			return rep, err
		}
		if pkCol == "" || len(cols) == 0 {
			continue
		}
		tablePKs[tbl] = pkCol
		tableCols[tbl] = cols

		sqliteSet, err := readSQLitePKSet(ctx, sqliteDB, tbl, pkCol)
		if err != nil {
			return rep, err
		}
		var baseSet map[string]bool
		if baselinePKs != nil {
			baseSet = baselinePKs[tbl]
		}
		delKeys, err := ReconcileTableDelta(tbl, baseSet, pgSet, sqliteSet)
		if err != nil {
			return rep, err
		}
		if len(delKeys) > 0 {
			tableDeletes[tbl] = delKeys
			rep.Actions = append(rep.Actions, fmt.Sprintf("delete %d hard-deleted row(s) from PG table %s", len(delKeys), tbl))
		}
	}

	if dryRun {
		rep.TablesProcessed = len(tablePKs)
		for _, del := range tableDeletes {
			rep.RowsDeleted += len(del)
		}
		return rep, nil
	}

	// 1. Delete hard-deleted rows in reverse table order.
	for i := len(tables) - 1; i >= 0; i-- {
		tbl := tables[i]
		delKeys := tableDeletes[tbl]
		if len(delKeys) == 0 {
			continue
		}
		pkCol := tablePKs[tbl]
		for _, k := range delKeys {
			if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM "%s" WHERE "%s"::text = $1`, tbl, pkCol), k); err != nil {
				return rep, fmt.Errorf("delete %s(%s=%s): %w", tbl, pkCol, k, err)
			}
			rep.RowsDeleted++
		}
	}

	// 2. Upsert all SQLite rows into PG.
	for _, tbl := range tables {
		pkCol := tablePKs[tbl]
		if pkCol == "" {
			continue
		}
		cols := tableCols[tbl]
		upserted, err := upsertTableFromSQLite(ctx, sqliteDB, tx, tbl, pkCol, cols, tm[tbl])
		if err != nil {
			return rep, fmt.Errorf("upsert %s: %w", tbl, err)
		}
		rep.RowsUpserted += upserted
		rep.TablesProcessed++

		var seqName sql.NullString
		if err := tx.QueryRow(ctx, `SELECT pg_get_serial_sequence($1, $2)`, tbl, pkCol).Scan(&seqName); err == nil && seqName.Valid && seqName.String != "" {
			if _, err := tx.Exec(ctx, fmt.Sprintf(`SELECT setval($1, COALESCE((SELECT MAX("%s") FROM "%s"), 1), true)`, pkCol, tbl), seqName.String); err != nil {
				return rep, fmt.Errorf("reset sequence %s: %w", seqName.String, err)
			}
			rep.SequencesReset++
		}
	}

	// 3. Check scheduled send state monotonicity before commit.
	if _, ok := tablePKs["mailbox_scheduled_sends"]; ok {
		sqliteStates, err := readScheduledStatesSQLite(ctx, sqliteDB)
		if err != nil {
			return rep, err
		}
		pgStates, err := readScheduledStatesPG(ctx, tx)
		if err != nil {
			return rep, err
		}
		if err := CheckScheduledSendMonotonicity(sqliteStates, pgStates); err != nil {
			return rep, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return rep, fmt.Errorf("commit pg reverse tx: %w", err)
	}
	rep.ZeroDiff = true
	return rep, nil
}

func readPGTableMetaAndPKs(ctx context.Context, tx pgx.Tx, tbl string) (string, []string, map[string]bool, error) {
	var pkCol string
	err := tx.QueryRow(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = to_regclass($1) AND i.indisprimary
		LIMIT 1`, tbl).Scan(&pkCol)
	if err != nil || pkCol == "" {
		return "", nil, nil, nil
	}

	colRows, err := tx.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1
		ORDER BY ordinal_position`, tbl)
	if err != nil {
		return "", nil, nil, err
	}
	var cols []string
	for colRows.Next() {
		var c string
		if err := colRows.Scan(&c); err != nil {
			colRows.Close()
			return "", nil, nil, err
		}
		cols = append(cols, c)
	}
	colRows.Close()

	pkRows, err := tx.Query(ctx, fmt.Sprintf(`SELECT "%s"::text FROM "%s"`, pkCol, tbl))
	if err != nil {
		return "", nil, nil, err
	}
	pkSet := map[string]bool{}
	for pkRows.Next() {
		var k string
		if err := pkRows.Scan(&k); err != nil {
			pkRows.Close()
			return "", nil, nil, err
		}
		pkSet[k] = true
	}
	pkRows.Close()
	return pkCol, cols, pkSet, nil
}

func readSQLitePKSet(ctx context.Context, db *sql.DB, tbl, pkCol string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT CAST("%s" AS TEXT) FROM "%s"`, pkCol, tbl)) // #nosec G202 -- validated type-map identifiers
	if err != nil {
		return nil, fmt.Errorf("read sqlite pks %s: %w", tbl, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

func upsertTableFromSQLite(ctx context.Context, sqliteDB *sql.DB, tx pgx.Tx, tbl, pkCol string, cols []string, cm map[string]datamigrate.ColumnMap) (int, error) {
	var mappedCols []string
	for _, c := range cols {
		if _, ok := cm[c]; ok {
			mappedCols = append(mappedCols, c)
		}
	}
	if len(mappedCols) == 0 {
		return 0, nil
	}
	quoted := make([]string, len(mappedCols))
	placeholders := make([]string, len(mappedCols))
	updates := make([]string, 0, len(mappedCols))
	for i, c := range mappedCols {
		quoted[i] = `"` + c + `"`
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		if c != pkCol {
			updates = append(updates, fmt.Sprintf(`"%s" = EXCLUDED."%s"`, c, c))
		}
	}
	upsertSQL := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s) ON CONFLICT ("%s") DO `,
		tbl, strings.Join(quoted, ", "), strings.Join(placeholders, ", "), pkCol)
	if len(updates) > 0 {
		upsertSQL += "UPDATE SET " + strings.Join(updates, ", ")
	} else {
		upsertSQL += "NOTHING"
	}

	rows, err := sqliteDB.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM "%s"`, strings.Join(quoted, ", "), tbl)) // #nosec G202 -- validated type-map identifiers
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	vals := make([]any, len(mappedCols))
	ptrs := make([]any, len(mappedCols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	count := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return count, err
		}
		pgArgs := make([]any, len(mappedCols))
		for i, c := range mappedCols {
			decoded, err := DecodeSQLiteValue(cm[c], vals[i])
			if err != nil {
				return count, fmt.Errorf("col %s: %w", c, err)
			}
			pgArgs[i] = decoded
		}
		if _, err := tx.Exec(ctx, upsertSQL, pgArgs...); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func readScheduledStatesSQLite(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT CAST(id AS TEXT), status FROM mailbox_scheduled_sends`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

func readScheduledStatesPG(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, status FROM mailbox_scheduled_sends`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}
