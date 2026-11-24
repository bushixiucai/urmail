package datamigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxpool "github.com/jackc/pgx/v5/pgxpool"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// pgSource adapts a pgxpool to the datamigrate.Source interface.
type pgSource struct{ pool *pgxpool.Pool }

func (s pgSource) Query(ctx context.Context, q string, args ...any) (Rows, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgxRowsWrap{Rows: rows}, nil
}

// pgxRowsWrap adapts pgx.Rows (Columns come from FieldDescriptions).
type pgxRowsWrap struct{ pgx.Rows }

func (w pgxRowsWrap) Columns() ([]string, error) {
	fds := w.FieldDescriptions()
	names := make([]string, len(fds))
	for i, fd := range fds {
		names[i] = string(fd.Name)
	}
	return names, nil
}

func (s pgSource) Exec(ctx context.Context, q string, args ...any) error {
	_, err := s.pool.Exec(ctx, q, args...)
	return err
}

func s5Fixture(t *testing.T) (context.Context, *pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	dsn := testdb.New(t)
	migrationURL, err := migrateURLFor(dsn)
	if err != nil {
		t.Fatalf("migrator dsn: %v", err)
	}
	migrator, err := db.NewMigrator(migrationURL)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("migrate source to head: %v", err)
	}
	if err := migrator.Close(); err != nil {
		t.Fatalf("close migrator: %v", err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool, dsn
}

func s5Target(t *testing.T) (*sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s5-target.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	if err := sqlitedb.Migrate(ctx, raw, os.DirFS("../.."), "migrations_sqlite"); err != nil {
		t.Fatalf("migrate target schema: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return raw, path
}

// TestS5MigratorFullSampleZeroDiff runs the complete §4 pipeline over a
// synthetic full-population sample: every type-map table receives generated
// rows on the PG source, the migrator copies them with preserved PKs, and
// the five gates prove zero difference (row hashes, schema diff, index diff,
// golden queries, negative isolation on the production scoped-querier
// assembly). The §4 additions are covered too: sqlite_sequence reset,
// idempotent rerun and the precheck gates.
func TestS5MigratorFullSampleZeroDiff(t *testing.T) {
	ctx, pool, _ := s5Fixture(t)
	rawTarget, targetPath := s5Target(t)
	defer rawTarget.Close()

	tm, err := LoadTypeMap(filepath.Join("..", "..", "migrations_sqlite", "type-map.tsv"))
	if err != nil {
		t.Fatalf("load type map: %v", err)
	}
	src := pgSource{pool: pool}

	sample := seedSyntheticSample(ctx, t, pool, tm, 3)

	// Explicit must-migrate set: every type-map table that exists in the
	// source catalog, minus engine metadata (schema_migrations) and the two
	// trigger-maintained tables (rows are created by engine triggers on
	// parents; the migrator still copies whatever exists on disk). The
	// coverage check is per-table set equality — no count thresholds.
	const recallRequestsGap = "PG pending-only INSERT vs SQLite preview+epoch-1-only insert: no representable form — migrator refuses with evidence (schema-port defect reported)"
	triggerMaintained := map[string]string{
		"platform_marketing_dispatch":  "singleton rows created by tenants triggers on PG",
		"mailbox_storage_reservations": "reservation rows created by mailboxes triggers on PG",
	}
	// Explicit ledger: trigger-maintained tables get NO filler seed — their
	// rows exist on the PG source because engine triggers create them during
	// parent seeding, and the migrator still copies them (counted via the
	// real source-row-count ledger below). recall_requests IS migrated
	// losslessly via the legal PG lifecycle (service pending insert + owner
	// preview authorization, attempt_epoch 0→1), see the table-level seeding
	// branch in the filler.
	plan, _, ierr := IntrospectSource(ctx, src)
	if ierr != nil {
		t.Fatalf("introspect: %v", ierr)
	}
	var expected []string
	for _, ti := range plan {
		if ti.Name == "schema_migrations" {
			continue // engine metadata
		}
		if _, mapped := tm[ti.Name]; !mapped {
			continue
		}
		expected = append(expected, ti.Name)
	}
	sort.Strings(expected)
	var expectedFillable []string
	var expectedTriggerMaintained []string
	for _, e := range expected {
		if triggerMaintained[e] != "" {
			expectedTriggerMaintained = append(expectedTriggerMaintained, e)
			continue
		}
		expectedFillable = append(expectedFillable, e)
	}
	var seededTables []string
	for table, n := range sample.Tables {
		if n > 0 {
			seededTables = append(seededTables, table)
		}
	}
	sort.Strings(expectedFillable)
	sort.Strings(expectedTriggerMaintained)
	sort.Strings(seededTables)
	if strings.Join(expectedFillable, ",") != strings.Join(seededTables, ",") {
		var missing, extra []string
		esp, sst := map[string]bool{}, map[string]bool{}
		for _, e := range expected {
			esp[e] = true
		}
		for _, s := range seededTables {
			sst[s] = true
		}
		for _, e := range expected {
			if !sst[e] {
				missing = append(missing, e)
			}
		}
		for _, s := range seededTables {
			if !esp[s] {
				extra = append(extra, s)
			}
		}
		t.Fatalf("seed coverage mismatch: missing=%v extra=%v failures=%v", missing, extra, sample.Failures)
	}
	// Trigger-maintained tables: rows exist on PG via engine triggers (no
	// filler seed); the migrator must still copy them.
	for _, table := range expectedTriggerMaintained {
		if sample.Tables[table] != 0 {
			t.Fatalf("trigger-maintained table %s unexpectedly filler-seeded", table)
		}
	}

	tgt := &Target{DB: rawTarget}
	// Let the synthetic preview's 2-second expiry elapse naturally: the PG
	// source then holds a genuinely expired preview (no trigger edits, no
	// deletes), proving the staging import keeps expired states verbatim.
	time.Sleep(2500 * time.Millisecond)
	// Small batch forces keyset pagination across pages (batch_size+1 rows
	// exist in multi-row tables).
	stats, err := Migrate(ctx, src, tgt, tm, Config{SkipTables: []string{"schema_migrations"}, BatchSize: 2})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if stats.VerifyOnly {
		t.Fatalf("unexpected verify-only run")
	}
	if len(stats.RecallRefused) != 0 {
		t.Fatalf("legal recall rows refused (%d): %v", len(stats.RecallRefused), stats.RecallRefused)
	}
	if stats.RecallMigrated == 0 || stats.RecallMigrated != stats.PerTable["recall_requests"] {
		t.Fatalf("recall preserved=%d perTable=%d", stats.RecallMigrated, stats.PerTable["recall_requests"])
	}
	if stats.RecallTableHash == "" || stats.RecallGuardHash == "" {
		t.Fatalf("missing recall hashes: %+v", stats)
	}
	if guard, err := readRecallGuard(ctx, tgt); err != nil || guard.Hash != stats.RecallGuardHash {
		t.Fatalf("target recall guard hash %v != import ledger %s", err, stats.RecallGuardHash)
	}
	if stats.Rows == 0 || stats.Tables < 90 {
		t.Fatalf("migrated tables=%d rows=%d; want >=90 tables and >0 rows", stats.Tables, stats.Rows)
	}
	// Per-table ledger: imported must equal the REAL source row count (filler
	// rows + trigger-generated rows on PG) — actual rows on disk, per contract.
	for _, table := range expected {
		var srcCount int64
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&srcCount); err != nil {
			t.Fatalf("source count %s: %v", table, err)
		}
		if stats.PerTable[table] != int(srcCount) {
			t.Fatalf("table %s: imported=%d != source rows=%d (every on-disk row must land)", table, stats.PerTable[table], srcCount)
		}
	}
	if stats.SequencesRese <= 0 {
		t.Fatalf("sequences reset=%d, want > 0", stats.SequencesRese)
	}

	hashes, err := VerifyRowHashes(ctx, src, tgt, tm, Config{SkipTables: []string{"schema_migrations"}})
	if err != nil {
		t.Fatalf("verify row hashes: %v", err)
	}
	if !hashes.RowHashZeroDiff {
		t.Fatalf("row hash diffs: %v", hashes.SchemaDiff)
	}
	if hashes.TablesCompared != len(expected)-1 {
		// recall_requests is verified by the dedicated VerifyRecallMigration
		// gate above, not the generic row-hash path.
		t.Fatalf("tables compared=%d, want %d (must-migrate minus recall_requests)", hashes.TablesCompared, len(expected)-1)
	}
	schemaDiff, err := DiffSchema(ctx, src, tgt, tm)
	if err != nil {
		t.Fatalf("schema diff: %v", err)
	}
	if len(schemaDiff) != 0 {
		t.Fatalf("schema diff non-empty (%d): %v", len(schemaDiff), schemaDiff[:minInt(5, len(schemaDiff))])
	}
	indexDiff, err := DiffIndexes(ctx, src, tgt, tm)
	if err != nil {
		t.Fatalf("index diff: %v", err)
	}
	if len(indexDiff) != 0 {
		t.Fatalf("unique index diff non-empty (%d): %v", len(indexDiff), indexDiff[:minInt(5, len(indexDiff))])
	}
	// Dedicated recall gate: explicit column-by-column comparison of recall
	// rows (generic hash path skipped for recall's lifecycle-specific
	// representation).
	recallDiffs, err := VerifyRecallMigration(ctx, src, tgt)
	if err != nil {
		t.Fatalf("recall verify: %v", err)
	}
	if len(recallDiffs) != 0 {
		t.Fatalf("recall migration diffs (%d): %v", len(recallDiffs), recallDiffs)
	}
	var pendingRecall, previewRecall int
	if err := rawTarget.QueryRowContext(ctx,
		"SELECT count(*) FROM recall_requests WHERE status='pending'").Scan(&pendingRecall); err != nil {
		t.Fatalf("recall pending count: %v", err)
	}
	if err := rawTarget.QueryRowContext(ctx,
		"SELECT count(*) FROM recall_requests WHERE status='preview'").Scan(&previewRecall); err != nil {
		t.Fatalf("recall preview count: %v", err)
	}
	if pendingRecall < 1 || previewRecall < 1 {
		t.Fatalf("staged recall states pending=%d preview=%d; want both legal states preserved", pendingRecall, previewRecall)
	}
	var expiredPreview, maxEpoch int
	if err := rawTarget.QueryRowContext(ctx,
		"SELECT count(*) FROM recall_requests WHERE status='preview' AND preview_expires_at < ?",
		time.Now().UTC().UnixMicro()).Scan(&expiredPreview); err != nil {
		t.Fatalf("recall expired preview count: %v", err)
	}
	if err := rawTarget.QueryRowContext(ctx, "SELECT max(attempt_epoch) FROM recall_requests").Scan(&maxEpoch); err != nil {
		t.Fatalf("recall max epoch: %v", err)
	}
	if expiredPreview < 1 || maxEpoch < 2 {
		t.Fatalf("staged expired previews=%d max_epoch=%d; want a naturally expired epoch-2 preview preserved",
			expiredPreview, maxEpoch)
	}
	var terminalCount, terminalStatuses int
	if err := rawTarget.QueryRowContext(ctx,
		"SELECT count(*), count(DISTINCT status) FROM recall_requests WHERE status IN ('failed','delivered','expired','uncertain')").Scan(&terminalCount, &terminalStatuses); err != nil {
		t.Fatalf("recall terminal counts: %v", err)
	}
	var localRecalled int
	if err := rawTarget.QueryRowContext(ctx, "SELECT count(*) FROM recall_requests WHERE status='local_recalled'").Scan(&localRecalled); err != nil {
		t.Fatalf("recall local_recalled count: %v", err)
	}
	if localRecalled < 1 {
		t.Fatalf("staged local_recalled rows=%d; want the legal product chain preserved", localRecalled)
	}
	if terminalStatuses < 3 {
		t.Fatalf("staged terminal status kinds=%d want >=3 (failed/expired/uncertain)", terminalStatuses)
	}
	t.Logf("recall extended witness expired_preview=%d max_epoch=%d terminal_rows=%d terminal_kinds=%d",
		expiredPreview, maxEpoch, terminalCount, terminalStatuses)
	t.Logf("recall staged states pending=%d preview=%d guard_hash=%s table_hash=%s refused=%d",
		pendingRecall, previewRecall, stats.RecallGuardHash, stats.RecallTableHash, len(stats.RecallRefused))

	// Target integrity: foreign_key_check=0 and integrity_check=ok.
	var fkViolations int
	if err := rawTarget.QueryRowContext(ctx, "PRAGMA foreign_key_check").Scan(&fkViolations); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign_key_check: %v", err)
	}
	if fkViolations != 0 {
		t.Fatalf("foreign_key_check violations=%d", fkViolations)
	}
	var integrity string
	if err := rawTarget.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check=%s, want ok", integrity)
	}

	// §3 time-variant semantics on marketing_quota_windows: ordering must be
	// -infinity < pre-epoch finite < epoch0 < future < +infinity; comparison
	// semantics: > now is exactly the future + +infinity rows.
	var qOrder string
	if err := rawTarget.QueryRowContext(ctx,
		"SELECT group_concat(id) FROM (SELECT id FROM marketing_quota_windows ORDER BY next_send_at, id)").Scan(&qOrder); err != nil {
		t.Fatalf("quota ordering: %v", err)
	}
	qIDs := strings.Split(qOrder, ",")
	if len(qIDs) != 5 {
		t.Fatalf("quota window rows=%d, want 5 time variants", len(qIDs))
	}
	var futureCount int
	if err := rawTarget.QueryRowContext(ctx,
		"SELECT count(*) FROM marketing_quota_windows WHERE next_send_at > ?",
		time.Now().UTC().UnixMicro()).Scan(&futureCount); err != nil {
		t.Fatalf("future count: %v", err)
	}
	if futureCount != 2 { // future finite + +infinity sentinel
		t.Fatalf("rows with next_send_at > now = %d, want 2 (future finite + +infinity sentinel)", futureCount)
	}

	golden := DefaultGoldenQueries(sample.TenantIDs[0])
	goldenRep, err := VerifyGoldenQueries(ctx, src, tgt, tm, golden)
	if err != nil {
		t.Fatalf("golden queries: %v", err)
	}
	if !goldenRep.GoldenZeroDiff {
		t.Fatalf("golden queries diverged over %d pairs: %v", goldenRep.GoldenQueries, goldenRep.SchemaDiff)
	}
	if err := NegativeIsolation(ctx, targetPath, sample.TenantIDs[0], sample.TenantIDs[len(sample.TenantIDs)-1], sample.SettingsKey); err != nil {
		t.Fatalf("negative isolation: %v", err)
	}

	// Idempotent rerun: the done marker routes to verify-only.
	stats2, err := Migrate(ctx, src, tgt, tm, Config{SkipTables: []string{"schema_migrations"}})
	if !errors.Is(err, ErrAlreadyMigrated) {
		t.Fatalf("rerun err=%v, want ErrAlreadyMigrated", err)
	}
	if !stats2.VerifyOnly {
		t.Fatalf("rerun must be verify-only")
	}
	hashes2, err := VerifyRowHashes(ctx, src, tgt, tm, Config{SkipTables: []string{"schema_migrations"}})
	if err != nil || !hashes2.RowHashZeroDiff {
		t.Fatalf("rerun verify: err=%v zeroDiff=%v diffs=%v", err, hashes2.RowHashZeroDiff, hashes2.SchemaDiff)
	}

	// sqlite_sequence reset: a fresh AUTOINCREMENT insert allocates above the
	// preserved PK maximum, never from 1.
	var maxCopied int64
	if err := rawTarget.QueryRowContext(ctx, "SELECT COALESCE(MAX(id),0) FROM users").Scan(&maxCopied); err != nil {
		t.Fatalf("max copied id: %v", err)
	}
	res, err := rawTarget.ExecContext(ctx,
		"INSERT INTO users(email,password_hash) VALUES('s5-after@example.test','x')")
	if err != nil {
		t.Fatalf("post-migration insert: %v", err)
	}
	newID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	if newID <= maxCopied {
		t.Fatalf("AUTOINCREMENT allocated %d, want > preserved max %d (sequence reset broken)", newID, maxCopied)
	}
}

// TestS5PrecheckNOCASEAbort proves the §4.3 gate: two emails that fold under
// lower() on a NOCASE unique column must abort the precheck with a conflict
// report; removing the conflict lets it pass.
// TestS5PrecheckNOCASEAbort verifies the NOCASE fold gate runs against the
// real schema: the ported schema defines NO case-insensitive unique columns
// (PG is case-sensitive unique), so the gate is structurally satisfied and
// must NOT abort on the seeded sample; the ±infinity sentinel audit must
// likewise pass (infinity rows map losslessly to the reserved sentinels) and
// the infinity-row counter must observe the DEFAULT-derived -infinity row.
// TestS5PrecheckNOCASEAbort verifies the §4.3/§4.4 precheck gates on the
// real schema and seeded sample: the ported schema defines NO case-insensitive
// unique columns (PG is case-sensitive unique), so the fold gate is
// structurally satisfied; the ±infinity sentinel audit must NOT abort on
// DEFAULT-derived -infinity or legal 9999-CE finite values (they map into
// int64 without collision); the ILIKE/LIKE inventory must capture the bare
// case-sensitive LIKE statements with their disposition.
func TestS5PrecheckNOCASEAbort(t *testing.T) {
	ctx, pool, _ := s5Fixture(t)
	raw, _ := s5Target(t)
	defer raw.Close()

	tm, err := LoadTypeMap(filepath.Join("..", "..", "migrations_sqlite", "type-map.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	seedSyntheticSample(ctx, t, pool, tm, 3)

	// A legal finite 9999-CE timestamp must NOT trip the sentinel audit
	// (MaxFiniteUnixMicro is a real int64, far from the int64max sentinel).
	if _, err := pool.Exec(ctx,
		"UPDATE marketing_quota_windows SET next_send_at='9999-12-31T23:59:59.999999+00' WHERE next_send_at='-infinity'"); err != nil {
		t.Fatalf("seed 9999 boundary: %v", err)
	}

	report, err := Precheck(ctx, pgSource{pool: pool}, &Target{DB: raw}, tm,
		[]string{filepath.Join("..", "..", "internal", "store", "queries_postgres")}, "")
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if report.Abort() != "" {
		t.Fatalf("precheck aborted on legal sample: %s (%+v)", report.Abort(), report)
	}
	if len(report.NOCASEConflicts) != 0 {
		t.Fatalf("NOCASE conflicts on a NOCASE-free schema: %v", report.NOCASEConflicts)
	}
	if len(report.SentinelCollisions) != 0 {
		t.Fatalf("sentinel collisions on a legal finite 9999 value: %v", report.SentinelCollisions)
	}
	if len(report.BareLikeStatements) == 0 {
		t.Fatalf("LIKE inventory empty — bare-LIKE disposition audit did not run")
	}
}

func TestS5StoreNewGate(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	var hits []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// The unified construction seam is the only sanctioned caller.
		if strings.HasSuffix(path, filepath.Join("internal", "db", "store.go")) {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for i, line := range strings.Split(string(data), "\n") {
			if regexp.MustCompile(`\bstore\.New\(`).MatchString(line) {
				hits = append(hits, fmt.Sprintf("%s:%d: %s", path, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("store.New non-test hits outside the sanctioned seam: %v", hits)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// syntheticSample records what the filler produced (for FK/golden wiring).
type syntheticSample struct {
	Tables      map[string]int
	Failures    map[string][]string // table -> constraint-rejection notes (not counted as rows)
	TenantIDs   []int64
	SettingsKey string
}

// migrateURLFor points the migrator at the fixture database with the
// migration role (mirrors authIntegrationDatabase in the handlers package).
func migrateURLFor(dsn string) (string, error) {
	target, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	params := target.Query()
	params.Set("role", "urmail_migrate")
	target.RawQuery = params.Encode()
	return target.String(), nil
}

// TestS5SQLitePeriodBoundary pins the dialect-corrected domain_tls_reports
// period CHECK on the SQLite target: normal report periods migrate, exactly
// eight days passes the inclusive bound, one microsecond beyond is rejected
// with no write. The constraint stays fully enforced (no bypass).
func TestS5SQLitePeriodBoundary(t *testing.T) {
	raw, _ := s5Target(t)
	defer raw.Close()
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO brands(id,slug,name) VALUES(1,'period-b','synthetic')`,
		`INSERT INTO users(id,email,password_hash) VALUES(1,'period@synthetic.test','x')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status,owner_user_id) VALUES(1,1,'PT','pt','active',1)`,
		`INSERT INTO domains(id,tenant_id,fqdn,status,verification_token,verified_at,provisioning_state) VALUES(1,1,'period.test','active','tok',1795337000000000,'ready')`,
	} {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	type tc struct {
		name    string
		offset  int64 // period_end = period_start + offset (micros)
		wantErr bool
	}
	cases := []tc{
		{"normal_one_hour", 3_600_000_000, false},
		{"exactly_eight_days", 691_200_000_000, false},
		{"eight_days_plus_one_micro", 691_200_000_001, true},
	}
	for i, c := range cases {
		start := int64(1800000000000000 + int64(i)*86_400_000_000)
		_, err := raw.ExecContext(ctx,
			`INSERT INTO domain_tls_reports(id, tenant_id, domain_id, report_digest, reporter_digest, report_id_digest, period_start, period_end, successful, failed, failures)
			 VALUES ($1,1,1,$2,$2,$2,$3,$4,1,0,'{}')`,
			100+int64(i), strings.Repeat("ab", 32), start, start+c.offset)
		if c.wantErr && err == nil {
			t.Fatalf("%s: out-of-bound period accepted (constraint bypassed)", c.name)
		}
		if !c.wantErr && err != nil {
			t.Fatalf("%s: valid period rejected: %v", c.name, err)
		}
		if c.wantErr {
			t.Logf("%s: rejected as required (err=%v) — no write", c.name, err)
		} else {
			t.Logf("%s: accepted as required", c.name)
		}
	}
	// Exactly one row per valid case persisted; the rejected case wrote none.
	var n int64
	if err := raw.QueryRowContext(ctx, "SELECT count(*) FROM domain_tls_reports").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("persisted rows=%d, want 2 (the two valid periods)", n)
	}
}

// TestS5FailureRollbackNoDone pins the recoverability contract: a mid-import
// failure leaves the target without the done marker and without any
// half-imported rows (transaction rollback), and a subsequent clean run
// completes and sets the done marker.
func TestS5FailureRollbackNoDone(t *testing.T) {
	ctx, pool, _ := s5Fixture(t)
	raw, _ := s5Target(t)
	defer raw.Close()

	tm, err := LoadTypeMap(filepath.Join("..", "..", "migrations_sqlite", "type-map.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	seedSyntheticSample(ctx, t, pool, tm, 2)
	src := pgSource{pool: pool}

	failing := &Target{DB: raw}
	if _, err := Migrate(ctx, src, failing, tm, Config{SkipTables: []string{"schema_migrations"}, BatchSize: 2, FailDuringTable: "mailboxes"}); err == nil {
		t.Fatalf("injected failure did not surface")
	}
	var done int64
	if err := raw.QueryRowContext(ctx, "SELECT count(*) FROM "+MetaTable+" WHERE state='done'").Scan(&done); err != nil {
		done = 0 // meta table itself rolled back
	}
	if done != 0 {
		t.Fatalf("done marker present after failed import")
	}
	var mailboxes int64
	_ = raw.QueryRowContext(ctx, "SELECT count(*) FROM mailboxes").Scan(&mailboxes)
	if mailboxes != 0 {
		t.Fatalf("half-imported rows survived rollback: mailboxes=%d", mailboxes)
	}

	// A clean rerun completes and lands all rows.
	ok2, err2 := Migrate(ctx, src, failing, tm, Config{SkipTables: []string{"schema_migrations"}})
	if err2 != nil {
		t.Fatalf("clean rerun: %v", err2)
	}
	if ok2.Rows == 0 {
		t.Fatalf("clean rerun migrated 0 rows")
	}
	var done2 int64
	if err := raw.QueryRowContext(ctx, "SELECT count(*) FROM "+MetaTable+" WHERE state='done'").Scan(&done2); err != nil {
		t.Fatal(err)
	}
	if done2 != 1 {
		t.Fatalf("done marker rows=%d after clean rerun, want 1", done2)
	}
}
