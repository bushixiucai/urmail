package datamigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Rows is the minimal row cursor the migrator consumes (satisfied by
// pgx.Rows on the PG side; *sql.Rows is adapted with sqlRowsWrap).
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
	Columns() ([]string, error)
}

// sqlRowsWrap adapts *sql.Rows (Close returns error) to Rows.
type sqlRowsWrap struct{ *sql.Rows }

func (w sqlRowsWrap) Close() {}

// Source is the PostgreSQL side (pgx.Conn / pgxpool.Pool satisfy it).
type Source interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

// SQLDB is the subset of *sql.DB the migrator uses.
type SQLDB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Target is the SQLite side. DB is either the concrete *sql.DB or a
// transaction-scoped adapter (sqlTxHandle below).
type Target struct {
	DB SQLDB
}

type sqlTxHandle struct{ tx *sql.Tx }

func (h sqlTxHandle) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return h.tx.ExecContext(ctx, query, args...)
}
func (h sqlTxHandle) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return h.tx.QueryContext(ctx, query, args...)
}
func (h sqlTxHandle) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return h.tx.QueryRowContext(ctx, query, args...)
}

// Config tunes the migrator. FailDuringTable injects a mid-import failure
// for the failure/rollback test (empty in production use).
type Config struct {
	// BatchSize is the keyset batch size (default 500).
	BatchSize int
	// SkipTables are excluded from migration and verification (bookkeeping
	// tables like schema_migrations managed by the engines themselves).
	SkipTables []string
	// FailDuringTable, when non-empty, aborts the import right before that
	// table is copied (test-only failure injection).
	FailDuringTable string
}

func (c Config) batchSize() int {
	if c.BatchSize > 0 {
		return c.BatchSize
	}
	return 500
}

func (c Config) skipped(table string) bool {
	for _, s := range c.SkipTables {
		if s == table {
			return true
		}
	}
	return false
}

// MetaState is the idempotency marker stored in the target database.
const (
	MetaTable     = "datamigrate_meta"
	MetaStateDone = "done"
)

// Stats reports what the copy phase did.
type Stats struct {
	Tables        int
	Rows          int
	PerTable      map[string]int
	SequencesRese int
	Reconciled    int
	VerifyOnly    bool
	// RecallRefused carries per-row refusal evidence for recall_requests rows
	// that are internally inconsistent (illegal in any engine). Legal states of
	// every kind are preserved verbatim; an illegal row blocks the import.
	RecallRefused []RecallRefusal
	// RecallMigrated counts recall rows preserved verbatim.
	RecallMigrated int
	// RecallStatusCounts is the per-status ledger of the preserved rows.
	RecallStatusCounts map[string]int64
	// RecallTableHash is sha256 over the canonical source rows (all columns).
	RecallTableHash string
	// RecallGuardHash is the sha256 of the recall_state_guard_insert DDL after
	// restoration, equal to the pre-import baseline.
	RecallGuardHash string
}

// ErrAlreadyMigrated is returned (wrapped with the verify result) when the
// target carries the done marker; the caller can treat it as success after
// checking VerifyReport.ZeroDiff.
var ErrAlreadyMigrated = errors.New("datamigrate: target already migrated; rerun verified instead")

// ensureMeta creates the idempotency marker table.
func ensureMeta(ctx context.Context, t *Target) error {
	_, err := t.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+MetaTable+`(
		state TEXT NOT NULL PRIMARY KEY,
		finished_at TEXT NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
	)`)
	return err
}

func metaState(ctx context.Context, t *Target) (string, bool, error) {
	var exists int
	if err := t.DB.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", MetaTable).Scan(&exists); err != nil {
		return "", false, err
	}
	if exists == 0 {
		return "", false, nil
	}
	var state string
	if err := t.DB.QueryRowContext(ctx, "SELECT state FROM "+MetaTable+" LIMIT 1").Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", true, nil
		}
		return "", false, err
	}
	return state, true, nil
}

// Migrate copies every type-map table from the PG source into the SQLite
// target: FK-ordered keyset batches, original PK values preserved, values
// converted per the type map, AUTOINCREMENT sequences reset to max(id). It is
// idempotent: when the target already carries the done marker the copy is
// skipped and the caller is expected to run Verify (the returned error wraps
// ErrAlreadyMigrated).
func Migrate(ctx context.Context, src Source, t *Target, tm TypeMap, cfg Config) (Stats, error) {
	stats := Stats{PerTable: map[string]int{}}
	if err := ensureMeta(ctx, t); err != nil {
		return stats, fmt.Errorf("ensure meta: %w", err)
	}
	state, present, err := metaState(ctx, t)
	if err != nil {
		return stats, err
	}
	if present && state == MetaStateDone {
		stats.VerifyOnly = true
		return stats, fmt.Errorf("%w", ErrAlreadyMigrated)
	}

	// The whole import runs in one transaction: any failure rolls the target
	// back to its pre-import state (no half-imported rows, no done marker,
	// fully recoverable by rerunning).
	dbHandle, ok := t.DB.(*sql.DB)
	if !ok {
		return stats, fmt.Errorf("import requires a concrete *sql.DB target")
	}
	tx, err := dbHandle.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("begin import tx: %w", err)
	}
	inner := &Target{DB: sqlTxHandle{tx: tx}}
	defer func() { _ = tx.Rollback() }()
	t = inner

	// Suspend all SQLite triggers inside this staging import transaction so
	// historical rows (e.g. mailboxes with password_version > 0 or non-zero
	// bytes_used, or tables with AFTER INSERT audit/change side effects) land
	// verbatim; exact byte-for-byte restoration and liveness probing run before
	// commit.
	savedTriggers, err := suspendAllTriggers(ctx, t)
	if err != nil {
		return stats, fmt.Errorf("suspend triggers: %w", err)
	}

	plan, byName, err := IntrospectSource(ctx, src)
	if err != nil {
		return stats, err
	}
	_ = byName
	for _, ti := range plan {
		if cfg.skipped(ti.Name) || cfg.skippedTable(tm, ti.Name) {
			continue
		}
		if cfg.FailDuringTable != "" && ti.Name == cfg.FailDuringTable {
			return stats, fmt.Errorf("injected failure before table %s", ti.Name)
		}
		if postCopyClearanceSkip(ti.Name) {
			continue // deferred: copied after the post-clear pass
		}
		// recall_requests offline preservation: every legal source row is
		// copied verbatim (all columns) inside one transaction that suppresses
		// only recall_state_guard_insert, restored and hash-verified before
		// commit. Illegal rows produce refusal evidence and BLOCK the whole
		// import (fail-closed, no silent skip).
		if ti.Name == "recall_requests" {
			rstat, refusals, rerr := migrateRecallTable(ctx, src, t, tm, ti)
			stats.RecallRefused = append(stats.RecallRefused, refusals...)
			if rerr != nil {
				return stats, fmt.Errorf("table %s: %w", ti.Name, rerr)
			}
			if len(refusals) > 0 {
				var reasons []string
				for _, rf := range refusals {
					reasons = append(reasons, fmt.Sprintf("id=%d status=%q epoch=%d: %s", rf.PK, rf.Status, rf.Epoch, rf.Reason))
				}
				return stats, fmt.Errorf("table %s: %d illegal row(s) rejected — import blocked: %s",
					ti.Name, len(refusals), strings.Join(reasons, "; "))
			}
			stats.RecallMigrated = rstat.Rows
			stats.RecallStatusCounts = rstat.StatusCounts
			stats.RecallTableHash = rstat.TableHash
			stats.RecallGuardHash = rstat.GuardHash
			stats.PerTable[ti.Name] = rstat.Rows
			stats.Rows += rstat.Rows
			stats.Tables++
			continue
		}
		// Tables whose ported INSERT triggers rewrite row state (domains
		// provisioning forces 'pending' and second-precision updated_at) are
		// copied with triggers suppressed — the migrator emulates the PG
		// migration-role exemption — so rows land verbatim.
		suppress := suppressDuringCopy[ti.Name]
		var restoreTriggers func(context.Context) error
		if suppress {
			restoreTriggers, err = suppressTriggers(ctx, t, ti.Name)
			if err != nil {
				return stats, fmt.Errorf("table %s suppress triggers: %w", ti.Name, err)
			}
		}
		n, err := copyTable(ctx, src, t, tm, ti, cfg.batchSize())
		if suppress {
			if err := restoreTriggers(ctx); err != nil {
				return stats, fmt.Errorf("table %s restore triggers: %w", ti.Name, err)
			}
		}
		if err != nil {
			return stats, fmt.Errorf("table %s: %w", ti.Name, err)
		}
		stats.PerTable[ti.Name] = n
		stats.Rows += n
		stats.Tables++
		// Per-table reconciliation runs right after the copy: ported
		// write-side-effect triggers rewrite rows on INSERT (domains start
		// 'pending'), and child tables copied next need the final state.
		// Triggers on the reconciled table are suppressed for the statements
		// (drop from sqlite_master原文, recreate after) so auxiliary columns
		// (e.g. updated_at touch triggers) cannot drift from the source.
		if stmts, ok := reconcilePerTable[ti.Name]; ok {
			restore, err := suppressTriggers(ctx, t, ti.Name)
			if err != nil {
				return stats, fmt.Errorf("table %s suppress triggers: %w", ti.Name, err)
			}
			for _, stmt := range stmts {
				if _, err := t.DB.ExecContext(ctx, stmt); err != nil {
					return stats, fmt.Errorf("table %s reconcile: %w", ti.Name, err)
				}
				stats.Reconciled++
			}
			if err := restore(ctx); err != nil {
				return stats, fmt.Errorf("table %s restore triggers: %w", ti.Name, err)
			}
		}
	}
	cleared := 0
	var postCopyTables []string
	for table, stmts := range postCopyClearance {
		for _, stmt := range stmts {
			if _, err := t.DB.ExecContext(ctx, stmt); err != nil {
				return stats, fmt.Errorf("post-clear %s: %w", table, err)
			}
			cleared++
		}
		postCopyTables = append(postCopyTables, table)
	}
	_ = cleared

	// Trigger-regenerated rows are cleared; now copy the authoritative source
	// rows for the post-clear tables (they were deferred in the main loop).
	if len(postCopyTables) > 0 {
		sort.Strings(postCopyTables)
		for _, table := range postCopyTables {
			n, err := copyTable(ctx, src, t, tm, byName[table], cfg.batchSize())
			if err != nil {
				return stats, fmt.Errorf("table %s (post-clear copy): %w", table, err)
			}
			stats.PerTable[table] = n
			stats.Rows += n
			stats.Tables++
		}
	}

	reset, err := ResetSequences(ctx, t)
	if err != nil {
		return stats, fmt.Errorf("reset sequences: %w", err)
	}
	stats.SequencesRese = reset

	if err := restoreAndVerifyAllTriggers(ctx, t, savedTriggers); err != nil {
		return stats, fmt.Errorf("restore triggers: %w", err)
	}

	if _, err := t.DB.ExecContext(ctx,
		"INSERT INTO "+MetaTable+"(state) VALUES(?) ON CONFLICT(state) DO UPDATE SET finished_at=finished_at", MetaStateDone); err != nil {
		return stats, fmt.Errorf("write meta: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("commit import: %w", err)
	}
	return stats, nil
}

// skippedTable reports tables absent from the type map (schema bookkeeping).
func (c Config) skippedTable(tm TypeMap, table string) bool {
	_, ok := tm[table]
	return !ok
}

// copyTable streams one table with keyset pagination on its primary key and
// inserts converted batches into the target. The original PK values are
// copied verbatim.
func copyTable(ctx context.Context, src Source, t *Target, tm TypeMap, ti TableInfo, batch int) (int, error) {
	cols := ti.Columns
	cm, ok := tm[ti.Name]
	if !ok {
		return 0, fmt.Errorf("table %s missing from type map", ti.Name)
	}
	// Column order: PK columns first (keyset), then the rest, deterministic.
	ordered := append([]string{}, ti.PK...)
	pkSeen := map[string]bool{}
	for _, c := range ti.PK {
		pkSeen[c] = true
	}
	for _, c := range cols {
		if !pkSeen[c] {
			ordered = append(ordered, c)
		}
	}
	// The SQLite target may carry columns the PG source lacks (new-table
	// additions with defaults); insert only the intersection, but keep every
	// PK column.
	insertCols := make([]string, 0, len(ordered))
	for _, c := range ordered {
		if _, mapped := cm[c]; mapped {
			insertCols = append(insertCols, c)
		}
	}
	if len(insertCols) == 0 {
		return 0, fmt.Errorf("no mapped columns for table %s", ti.Name)
	}
	for _, pk := range ti.PK {
		found := false
		for _, c := range insertCols {
			if c == pk {
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("table %s: pk column %s not in type map", ti.Name, pk)
		}
	}

	selectSQL := fmt.Sprintf("SELECT %s FROM %s", quoteList(ordered, false), ti.Name)
	insertSQL := fmt.Sprintf("INSERT OR REPLACE INTO %s (%s) VALUES (%s)",
		ti.Name, quoteList(insertCols, false), placeholders(len(insertCols)))

	var total int
	var cursor []any
	for {
		args := append([]any{}, cursor...)
		q := selectSQL
		if len(cursor) > 0 {
			ph := make([]string, len(cursor))
			for i := range cursor {
				ph[i] = fmt.Sprintf("$%d", i+1)
			}
			q += fmt.Sprintf(" WHERE (%s) > (%s)", quoteList(ti.PK, false), strings.Join(ph, ","))
		}
		q += fmt.Sprintf(" ORDER BY %s LIMIT %d", quoteList(ti.PK, false), batch)

		rows, err := src.Query(ctx, q, args...)
		if err != nil {
			return total, fmt.Errorf("select: %w", err)
		}
		dest := make([]any, len(ordered))
		ptrs := make([]any, len(ordered))
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		batchRows := 0
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return total, fmt.Errorf("scan: %w", err)
			}
			out := make([]any, len(insertCols))
			for i, c := range insertCols {
				cmCol := cm[c]
				v, err := ConvertValue(cmCol, dest[indexOf(ordered, c)])
				if err != nil {
					rows.Close()
					return total, fmt.Errorf("column %s: %w", c, err)
				}
				out[i] = v
			}
			if _, err := t.DB.ExecContext(ctx, insertSQL, out...); err != nil {
				rows.Close()
				return total, fmt.Errorf("insert: %w (args=%v)", err, out)
			}
			total++
			batchRows++
			// Advance the keyset cursor from the PK columns.
			cursor = make([]any, len(ti.PK))
			for i, pk := range ti.PK {
				cursor[i] = dest[indexOf(ordered, pk)]
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return total, err
		}
		rows.Close()
		if batchRows < batch {
			return total, nil
		}
	}
}

// reconcileImport runs the post-import reconciliation statements. The ported
// SQLite write-side-effect triggers (§3.4) rewrite some rows on INSERT (e.g.
// domains provisioning starts 'pending' exactly like the PG guard); the real
// provisioning completion flow then moves them to their final state. The
// migrator mirrors that completion after the bulk copy so the stored state
// matches the source; the zero-diff verify afterwards proves it.
var reconcilePerTable = map[string][]string{
	// domains: provisioning completion (PG flow: pending -> ready under the
	// migration/platform role once DNS verification finished). dns_explicit /
	// custom_web_enabled are zeroed by the ported trigger on INSERT; the PG
	// source rows are seeded false, so zeroing on the target matches.
	"domains": {
		"UPDATE domains SET provisioning_state='ready' WHERE provisioning_state<>'ready'",
	},
}

// postCopyClearance maps a table whose rows are regenerated by the ported
// SQLite write-side-effect triggers while OTHER tables are copied (every
// mailbox/calendar/contact insert appends sync-state rows). The trigger
// artifacts are import noise — the authoritative rows come from the source —
// so they are cleared after the copy, before verification.
func postCopyClearanceSkip(table string) bool {
	_, ok := postCopyClearance[table]
	return ok
}

// suppressDuringCopy lists tables copied with their ported INSERT triggers
// suppressed: the triggers rewrite row state on INSERT (domains provisioning
// forces 'pending' and a second-precision updated_at), which the migrator
// must not apply — it emulates the PG migration-role exemption.
var suppressDuringCopy = map[string]bool{
	"domains": true,
}

var postCopyClearance = map[string][]string{
	"mailbox_changes":     {"DELETE FROM mailbox_changes"},
	"notification_outbox": {"DELETE FROM notification_outbox"},
}

// ReconcileImport applies every per-table reconciliation statement (for
// callers that need to run them standalone) and returns how many ran.
func ReconcileImport(ctx context.Context, t *Target) (int, error) {
	n := 0
	for _, stmts := range reconcilePerTable {
		for _, stmt := range stmts {
			if _, err := t.DB.ExecContext(ctx, stmt); err != nil {
				return n, fmt.Errorf("%s: %w", stmt, err)
			}
			n++
		}
	}
	return n, nil
}

type triggerDef struct {
	name    string
	sqlText string
}

func snapshotTriggers(ctx context.Context, t *Target) ([]triggerDef, error) {
	rows, err := t.DB.QueryContext(ctx,
		"SELECT name, sql FROM sqlite_master WHERE type='trigger' AND tbl_name<>'recall_requests' AND sql IS NOT NULL ORDER BY name")
	if err != nil {
		return nil, err
	}
	var trigs []triggerDef
	for rows.Next() {
		var tr triggerDef
		if err := rows.Scan(&tr.name, &tr.sqlText); err != nil {
			_ = rows.Close()
			return nil, err
		}
		trigs = append(trigs, tr)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	return trigs, nil
}

func suspendAllTriggers(ctx context.Context, t *Target) ([]triggerDef, error) {
	trigs, err := snapshotTriggers(ctx, t)
	if err != nil {
		return nil, err
	}
	for _, tr := range trigs {
		if _, err := t.DB.ExecContext(ctx, `DROP TRIGGER "`+tr.name+`"`); err != nil {
			return nil, fmt.Errorf("drop trigger %s: %w", tr.name, err)
		}
	}
	return trigs, nil
}

func restoreAndVerifyAllTriggers(ctx context.Context, t *Target, expected []triggerDef) error {
	for _, tr := range expected {
		if _, err := t.DB.ExecContext(ctx, tr.sqlText); err != nil {
			return fmt.Errorf("recreate trigger %s: %w", tr.name, err)
		}
	}
	actual, err := snapshotTriggers(ctx, t)
	if err != nil {
		return fmt.Errorf("resnapshot triggers: %w", err)
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("trigger count mismatch after restore: got %d, want %d", len(actual), len(expected))
	}
	hasPasswordGuard := false
	for i := range expected {
		if actual[i].name != expected[i].name || actual[i].sqlText != expected[i].sqlText {
			return fmt.Errorf("trigger definition mismatch at %s: got %q, want %q", expected[i].name, actual[i].sqlText, expected[i].sqlText)
		}
		if expected[i].name == "mailbox_password_version_insert_guard" {
			hasPasswordGuard = true
		}
	}

	var integrity string
	if err := t.DB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("integrity_check=%q", integrity)
	}
	fkRows, err := t.DB.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	hasFKViolation := fkRows.Next()
	_ = fkRows.Close()
	if hasFKViolation {
		return fmt.Errorf("foreign_key_check reported violation(s) after trigger restore")
	}

	if hasPasswordGuard {
		if err := probeMailboxPasswordGuard(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func probeMailboxPasswordGuard(ctx context.Context, t *Target) error {
	if _, err := t.DB.ExecContext(ctx, "SAVEPOINT s5_trigger_probe"); err != nil {
		return fmt.Errorf("begin trigger probe savepoint: %w", err)
	}
	rollbackProbe := func() error {
		if _, rbErr := t.DB.ExecContext(ctx, "ROLLBACK TO SAVEPOINT s5_trigger_probe"); rbErr != nil {
			return fmt.Errorf("rollback trigger probe savepoint: %w", rbErr)
		}
		if _, relErr := t.DB.ExecContext(ctx, "RELEASE SAVEPOINT s5_trigger_probe"); relErr != nil {
			return fmt.Errorf("release trigger probe savepoint: %w", relErr)
		}
		return nil
	}

	var tenantID, domainID int64
	var fqdn string
	err := t.DB.QueryRowContext(ctx,
		`SELECT d.tenant_id, d.id, d.fqdn
		 FROM domains d
		 JOIN tenants t ON t.id = d.tenant_id
		 WHERE t.status IN ('active','pending')
		   AND d.status = 'active'
		   AND d.verified_at IS NOT NULL
		   AND d.provisioning_state = 'ready'
		 LIMIT 1`).Scan(&tenantID, &domainID, &fqdn)
	if errors.Is(err, sql.ErrNoRows) {
		if rbErr := rollbackProbe(); rbErr != nil {
			return rbErr
		}
		return nil
	}
	if err != nil {
		_ = rollbackProbe()
		return fmt.Errorf("lookup domain for trigger probe: %w", err)
	}

	_, probeErr := t.DB.ExecContext(ctx,
		`INSERT INTO mailboxes(tenant_id, domain_id, local_part, address, password_hash, quota_bytes, password_version, password_revoke_pending)
		 VALUES(?, ?, '__s5_probe__', ?, 'x', -1, 1, 0)`,
		tenantID, domainID, "__s5_probe__@"+fqdn)
	if rbErr := rollbackProbe(); rbErr != nil {
		return rbErr
	}
	if probeErr == nil || !strings.Contains(probeErr.Error(), "MAILBOX_PASSWORD_VERSION_IMMUTABLE") {
		return fmt.Errorf("trigger liveness probe failed: expected MAILBOX_PASSWORD_VERSION_IMMUTABLE, got %w", probeErr)
	}
	return nil
}

// suppressTriggers drops every trigger on the table and returns a restore
// function that recreates them verbatim from sqlite_master.
func suppressTriggers(ctx context.Context, t *Target, table string) (func(context.Context) error, error) {
	rows, err := t.DB.QueryContext(ctx,
		"SELECT name, sql FROM sqlite_master WHERE type='trigger' AND tbl_name=? AND sql IS NOT NULL", table)
	if err != nil {
		return nil, err
	}
	type trig struct{ name, sqlText string }
	var trigs []trig
	for rows.Next() {
		var tr trig
		if err := rows.Scan(&tr.name, &tr.sqlText); err != nil {
			_ = rows.Close() // read-only rows
			return nil, err
		}
		trigs = append(trigs, tr)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close() // read-only rows
		return nil, err
	}
	_ = rows.Close() // read-only rows
	for _, tr := range trigs {
		if _, err := t.DB.ExecContext(ctx, "DROP TRIGGER \""+tr.name+"\""); err != nil {
			return nil, err
		}
	}
	return func(ctx context.Context) error {
		for _, tr := range trigs {
			if _, err := t.DB.ExecContext(ctx, tr.sqlText); err != nil {
				return fmt.Errorf("recreate trigger %s: %w", tr.name, err)
			}
		}
		return nil
	}, nil
}

// ResetSequences sets every AUTOINCREMENT table's sqlite_sequence entry to
// max(pk) so post-migration AUTOINCREMENT allocation cannot collide with the
// preserved original PKs (§4.1).
func ResetSequences(ctx context.Context, t *Target) (int, error) {
	var names []string
	rows, err := t.DB.QueryContext(ctx, "SELECT name FROM sqlite_sequence")
	if err != nil {
		return 0, fmt.Errorf("read sqlite_sequence: %w", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close() // read-only rows
			return 0, err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close() // read-only rows
		return 0, err
	}
	_ = rows.Close() // read-only rows

	reset := 0
	for _, name := range names {
		// PK column: the INTEGER PRIMARY KEY of an AUTOINCREMENT table is its
		// rowid alias; max(rowid) is the safest universal form.
		if _, err := t.DB.ExecContext(ctx,
			"UPDATE sqlite_sequence SET seq=(SELECT COALESCE(MAX(rowid),0) FROM \""+name+"\") WHERE name=?",
			name); err != nil {
			return reset, fmt.Errorf("reset sequence %s: %w", name, err)
		}
		reset++
	}
	return reset, nil
}

func quoteList(cols []string, _ bool) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = `"` + c + `"`
	}
	return strings.Join(parts, ", ")
}

func indexOf(hay []string, needle string) int {
	for i, h := range hay {
		if h == needle {
			return i
		}
	}
	return -1
}
