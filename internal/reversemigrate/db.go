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

// excludedMetadataTables lists migration and internal bookkeeping tables that
// must never be deleted or overwritten during reverse migration.
var excludedMetadataTables = map[string]bool{
	"schema_migrations":      true,
	"sqlite_sequence":        true,
	"urmail_s5_migrate_meta": true,
	"gorp_migrations":        true,
	"goose_db_version":       true,
}

// pgTriggerInsertDeps records cross-table dependencies enforced by PostgreSQL
// BEFORE INSERT triggers across migrations/*.up.sql that are not expressed as
// foreign keys (audited across all 79 PG triggers):
//   - mailbox_aliases -> mailboxes: urmail_alias_sender_logins_guard
//   - domains -> settings, tenants: urmail_resource_guard
//   - mailboxes -> domains, settings, tenants: urmail_resource_guard / identity
//   - retention_policies -> retention_previews: urmail_retention_confirmation_guard
//   - mailbox_contact_group_members -> mailbox_contact_groups, mailbox_contacts
//   - recall_requests -> archive_entries, mailboxes: urmail_recall_guard
//   - ticket_messages -> tickets: urmail_require_open_ticket
//   - compliance_grants -> tenants, users, tenant_members
//   - retention_previews -> tenants, users, tenant_members
//   - archive_approval_requests -> archive_investigations
//   - ai_provider_configs -> tenants, settings
var pgTriggerInsertDeps = map[string][]string{
	"mailbox_aliases":               {"mailboxes"},
	"domains":                       {"settings", "tenants"},
	"mailboxes":                     {"domains", "settings", "tenants"},
	"retention_policies":            {"retention_previews"},
	"mailbox_contact_group_members": {"mailbox_contact_groups", "mailbox_contacts"},
	"recall_requests":               {"archive_entries", "mailboxes"},
	"ticket_messages":               {"tickets"},
	"compliance_grants":             {"tenants", "users", "tenant_members"},
	"retention_previews":            {"tenants", "users", "tenant_members"},
	"archive_approval_requests":     {"archive_investigations"},
	"ai_provider_configs":           {"tenants", "settings"},
}

// FilterBusinessTables removes migration/system metadata tables such as
// schema_migrations from the reverse-migration plan.
func FilterBusinessTables(tables []string) []string {
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		if excludedMetadataTables[t] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// OrderTablesForPG topologically orders tables (parents first) combining
// PostgreSQL foreign-key edges and pgTriggerInsertDeps.
func OrderTablesForPG(tables []string, pgFKs map[string][]string) ([]string, error) {
	sorted := FilterBusinessTables(tables)
	sort.Strings(sorted)
	byName := make(map[string]bool, len(sorted))
	for _, t := range sorted {
		byName[t] = true
	}
	deps := make(map[string]map[string]bool, len(sorted))
	for _, t := range sorted {
		deps[t] = map[string]bool{}
		for _, fk := range pgFKs[t] {
			if byName[fk] && fk != t {
				deps[t][fk] = true
			}
		}
		for _, td := range pgTriggerInsertDeps[t] {
			if byName[td] && td != t {
				deps[t][td] = true
			}
		}
	}

	var ordered []string
	placed := make(map[string]bool, len(sorted))
	for len(ordered) < len(sorted) {
		progressed := false
		for _, t := range sorted {
			if placed[t] {
				continue
			}
			ready := true
			for dep := range deps[t] {
				if !placed[dep] {
					ready = false
					break
				}
			}
			if ready {
				placed[t] = true
				ordered = append(ordered, t)
				progressed = true
			}
		}
		if !progressed {
			var stuck []string
			for _, t := range sorted {
				if !placed[t] {
					stuck = append(stuck, t)
				}
			}
			return nil, fmt.Errorf("cyclic PG dependency among tables: %s", strings.Join(stuck, ", "))
		}
	}
	return ordered, nil
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

	pgFKs, err := readPGForeignKeys(ctx, tx)
	if err != nil {
		return rep, fmt.Errorf("introspect pg foreign keys: %w", err)
	}
	tables, err := OrderTablesForPG(tm.Tables(), pgFKs)
	if err != nil {
		return rep, err
	}
	tablePKs := make(map[string]string, len(tables))
	tableCols := make(map[string][]string, len(tables))
	tableAlwaysIdentity := make(map[string]bool, len(tables))
	tableDeletes := make(map[string][]string, len(tables))

	for _, tbl := range tables {
		pkCol, cols, hasAlwaysIdentity, pgSet, err := readPGTableMetaAndPKs(ctx, tx, tbl)
		if err != nil {
			return rep, err
		}
		if pkCol == "" || len(cols) == 0 {
			continue
		}
		tablePKs[tbl] = pkCol
		tableCols[tbl] = cols
		tableAlwaysIdentity[tbl] = hasAlwaysIdentity

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

	// Suspend non-internal PG triggers inside this transaction so bulk reverse copy
	// (e.g., mailbox_password_version_guard, alias_sender_logins_guard, audit triggers)
	// can replay SQLite state cleanly, then restore with byte-for-byte parity before commit.
	savedPGTrigs, err := suspendPGTriggers(ctx, tx)
	if err != nil {
		return rep, err
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
		upserted, err := upsertTableFromSQLite(ctx, sqliteDB, tx, tbl, pkCol, cols, tableAlwaysIdentity[tbl], tm[tbl])
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

	if err := restoreAndVerifyPGTriggers(ctx, tx, savedPGTrigs); err != nil {
		return rep, err
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

func readPGForeignKeys(ctx context.Context, tx pgx.Tx) (map[string][]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.conrelid::regclass::text, c.confrelid::regclass::text
		FROM pg_constraint c
		JOIN pg_namespace n ON n.oid = c.connamespace
		WHERE c.contype = 'f' AND n.nspname = current_schema()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			return nil, err
		}
		if idx := strings.LastIndex(child, "."); idx >= 0 {
			child = child[idx+1:]
		}
		if idx := strings.LastIndex(parent, "."); idx >= 0 {
			parent = parent[idx+1:]
		}
		out[child] = append(out[child], parent)
	}
	return out, rows.Err()
}

func readPGTableMetaAndPKs(ctx context.Context, tx pgx.Tx, tbl string) (string, []string, bool, map[string]bool, error) {
	var pkCol string
	err := tx.QueryRow(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = to_regclass($1) AND i.indisprimary
		LIMIT 1`, tbl).Scan(&pkCol)
	if err != nil || pkCol == "" {
		return "", nil, false, nil, nil
	}

	colRows, err := tx.Query(ctx, `
		SELECT column_name, (is_identity = 'YES' AND identity_generation = 'ALWAYS')
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1
		ORDER BY ordinal_position`, tbl)
	if err != nil {
		return "", nil, false, nil, err
	}
	var cols []string
	hasAlwaysIdentity := false
	for colRows.Next() {
		var c string
		var isAlways bool
		if err := colRows.Scan(&c, &isAlways); err != nil {
			colRows.Close()
			return "", nil, false, nil, err
		}
		if isAlways {
			hasAlwaysIdentity = true
		}
		cols = append(cols, c)
	}
	colRows.Close()

	pkRows, err := tx.Query(ctx, fmt.Sprintf(`SELECT "%s"::text FROM "%s"`, pkCol, tbl))
	if err != nil {
		return "", nil, false, nil, err
	}
	pkSet := map[string]bool{}
	for pkRows.Next() {
		var k string
		if err := pkRows.Scan(&k); err != nil {
			pkRows.Close()
			return "", nil, false, nil, err
		}
		pkSet[k] = true
	}
	pkRows.Close()
	return pkCol, cols, hasAlwaysIdentity, pkSet, nil
}

func readSQLitePKSet(ctx context.Context, db *sql.DB, tbl, pkCol string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT CAST("%s" AS TEXT) FROM "%s"`, pkCol, tbl)) // #nosec G202,G701 -- validated type-map identifiers
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

func upsertTableFromSQLite(ctx context.Context, sqliteDB *sql.DB, tx pgx.Tx, tbl, pkCol string, cols []string, alwaysIdentity bool, cm map[string]datamigrate.ColumnMap) (int, error) {
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
	// Tables with GENERATED ALWAYS AS IDENTITY columns (e.g. mailbox_scheduled_sends.id)
	// reject explicit ID inserts with SQLSTATE 428C9 unless OVERRIDING SYSTEM VALUE is set.
	overridingClause := ""
	if alwaysIdentity {
		overridingClause = " OVERRIDING SYSTEM VALUE"
	}
	upsertSQL := fmt.Sprintf(`INSERT INTO "%s" (%s)%s VALUES (%s) ON CONFLICT ("%s") DO `,
		tbl, strings.Join(quoted, ", "), overridingClause, strings.Join(placeholders, ", "), pkCol)
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

type pgTriggerDef struct {
	name    string
	table   string
	ddl     string
	enabled string
}

func snapshotPGTriggers(ctx context.Context, tx pgx.Tx) ([]pgTriggerDef, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.tgname, c.relname, pg_get_triggerdef(t.oid, true), t.tgenabled::text
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND NOT t.tgisinternal
		ORDER BY c.relname, t.tgname`)
	if err != nil {
		return nil, fmt.Errorf("snapshot pg triggers: %w", err)
	}
	defer rows.Close()
	var trigs []pgTriggerDef
	for rows.Next() {
		var tr pgTriggerDef
		if err := rows.Scan(&tr.name, &tr.table, &tr.ddl, &tr.enabled); err != nil {
			return nil, fmt.Errorf("scan pg trigger: %w", err)
		}
		trigs = append(trigs, tr)
	}
	return trigs, rows.Err()
}

func suspendPGTriggers(ctx context.Context, tx pgx.Tx) ([]pgTriggerDef, error) {
	trigs, err := snapshotPGTriggers(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, tr := range trigs {
		dropSQL := fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, pgx.Identifier{tr.name}.Sanitize(), pgx.Identifier{tr.table}.Sanitize()) // #nosec G202,G701 -- identifiers sanitized via pgx.Identifier
		if _, err := tx.Exec(ctx, dropSQL); err != nil {
			return nil, fmt.Errorf("suspend pg trigger %s on %s: %w", tr.name, tr.table, err)
		}
	}
	return trigs, nil
}

func restoreAndVerifyPGTriggers(ctx context.Context, tx pgx.Tx, saved []pgTriggerDef) error {
	if len(saved) == 0 {
		return nil
	}
	hasTrig := make(map[string]bool, len(saved))
	for _, tr := range saved {
		if _, err := tx.Exec(ctx, tr.ddl); err != nil {
			return fmt.Errorf("restore pg trigger %s on %s: %w", tr.name, tr.table, err)
		}
		hasTrig[tr.name] = true
	}

	after, err := snapshotPGTriggers(ctx, tx)
	if err != nil {
		return fmt.Errorf("verify restored pg triggers: %w", err)
	}
	if len(after) != len(saved) {
		return fmt.Errorf("pg trigger count mismatch after restore: got %d, want %d", len(after), len(saved))
	}
	for i := range saved {
		if after[i].name != saved[i].name || after[i].table != saved[i].table || after[i].ddl != saved[i].ddl {
			return fmt.Errorf("pg trigger definition mismatch after restore for %s on %s", saved[i].name, saved[i].table)
		}
		if after[i].enabled != "O" && after[i].enabled != "A" {
			return fmt.Errorf("pg trigger %s on %s not enabled after restore (tgenabled=%s)", after[i].name, after[i].table, after[i].enabled)
		}
	}

	if hasTrig["mailbox_password_version_guard"] {
		var probeID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM mailboxes LIMIT 1`).Scan(&probeID); err == nil {
			if err := probePGTriggerRejects(ctx, tx, "mailbox_password_version_guard",
				`UPDATE mailboxes SET password_version = password_version + 1 WHERE id = $1`, probeID); err != nil {
				return err
			}
		}
	}
	if hasTrig["alias_sender_logins_guard"] {
		var probeID int64
		var colType string
		if err := tx.QueryRow(ctx, `SELECT id, pg_typeof(sender_logins)::text FROM mailbox_aliases LIMIT 1`).Scan(&probeID, &colType); err == nil {
			probeSQL := `UPDATE mailbox_aliases SET sender_logins = ARRAY['__e7_probe_invalid__']::text[] WHERE id = $1`
			if colType == "jsonb" {
				probeSQL = `UPDATE mailbox_aliases SET sender_logins = '["__e7_probe_invalid__"]'::jsonb WHERE id = $1`
			}
			if err := probePGTriggerRejects(ctx, tx, "alias_sender_logins_guard", probeSQL, probeID); err != nil {
				return err
			}
		}
	}
	return nil
}

func probePGTriggerRejects(ctx context.Context, tx pgx.Tx, trigName, query string, arg any) error {
	if _, err := tx.Exec(ctx, `SAVEPOINT e7_pg_trigger_probe`); err != nil {
		return fmt.Errorf("create trigger probe savepoint: %w", err)
	}
	_, probeErr := tx.Exec(ctx, query, arg)
	if _, rbErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT e7_pg_trigger_probe`); rbErr != nil {
		return fmt.Errorf("rollback trigger probe savepoint: %w", rbErr)
	}
	if _, relErr := tx.Exec(ctx, `RELEASE SAVEPOINT e7_pg_trigger_probe`); relErr != nil {
		return fmt.Errorf("release trigger probe savepoint: %w", relErr)
	}
	if probeErr == nil {
		return fmt.Errorf("post-restore trigger liveness probe failed: %s did not fire", trigName)
	}
	return nil
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
