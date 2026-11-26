package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bushixiucai/urmail/internal/datamigrate"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	sqlmigrations "github.com/bushixiucai/urmail/migrations_sqlite"
)

type cliPGSource struct{ pool *pgxpool.Pool }

func (s cliPGSource) Query(ctx context.Context, q string, args ...any) (datamigrate.Rows, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return cliPGXRows{Rows: rows}, nil
}

type cliPGXRows struct{ pgx.Rows }

func (w cliPGXRows) Columns() ([]string, error) {
	fds := w.FieldDescriptions()
	names := make([]string, len(fds))
	for i, fd := range fds {
		names[i] = fd.Name
	}
	return names, nil
}

// sqliteImportReport is the structured JSON output emitted by `urmailctl sqlite import`
// and `urmailctl sqlite verify-import`.
type sqliteImportReport struct {
	Mode              string   `json:"mode"`
	Target            string   `json:"target"`
	IdempotentRerun   bool     `json:"idempotent_rerun"`
	TablesMigrated    int      `json:"tables_migrated"`
	RowsMigrated      int      `json:"rows_migrated"`
	SequencesReset    int      `json:"sequences_reset"`
	TablesCompared    int      `json:"tables_compared"`
	RowsCompared      int      `json:"rows_compared"`
	RowHashZeroDiff   bool     `json:"row_hash_zero_diff"`
	RowHashDiffs      []string `json:"row_hash_diffs,omitempty"`
	SchemaDiffCount   int      `json:"schema_diff_count"`
	IndexDiffCount    int      `json:"index_diff_count"`
	RecallDiffCount   int      `json:"recall_diff_count"`
	GoldenQueries     int      `json:"golden_queries"`
	GoldenZeroDiff    bool     `json:"golden_zero_diff"`
	ForeignKeyIssues  int      `json:"foreign_key_issues"`
	IntegrityCheck    string   `json:"integrity_check"`
	ZeroDiff          bool     `json:"zero_diff"`
	PrecheckAbort     string   `json:"precheck_abort,omitempty"`
	NOCASEConflicts   []string `json:"nocase_conflicts,omitempty"`
	NumericUsageCount int      `json:"numeric_usage_count"`
}

func runSQLiteImport(ctx context.Context, args []string, defaultVerifyOnly bool, output io.Writer) error {
	name := "sqlite import"
	if defaultVerifyOnly {
		name = "sqlite verify-import"
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sourceEnv := flags.String("source-env", "URMAIL_MIGRATE_SOURCE_URL", "environment variable holding the PostgreSQL source URL")
	target := flags.String("target", "", "SQLite target database path (absolute)")
	migrationsDir := flags.String("migrations-dir", "", "explicit migrations_sqlite directory (default: embedded chain)")
	batchSize := flags.Int("batch-size", 500, "keyset batch size")
	verifyOnly := flags.Bool("verify-only", defaultVerifyOnly, "verify 0-diff without writing to target")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: urmailctl %s --target PATH [--source-env VAR] [--migrations-dir DIR] [--batch-size N] [--verify-only]", name)
	}
	if *target == "" || !filepath.IsAbs(*target) || filepath.Clean(*target) != *target {
		return fmt.Errorf("%s requires a clean absolute --target PATH", name)
	}
	if *sourceEnv == "" {
		return fmt.Errorf("%s requires a non-empty --source-env name", name)
	}
	sourceURL := os.Getenv(*sourceEnv)
	if sourceURL == "" {
		return fmt.Errorf("%s: environment variable %s is empty", name, *sourceEnv)
	}

	var tm datamigrate.TypeMap
	var err error
	if *migrationsDir != "" {
		tm, err = datamigrate.LoadTypeMap(filepath.Join(*migrationsDir, "type-map.tsv"))
	} else {
		tm, err = datamigrate.ParseTypeMap(bytes.NewReader(sqlmigrations.TypeMapTSV), "migrations_sqlite/type-map.tsv")
	}
	if err != nil {
		return fmt.Errorf("load type map: %w", err)
	}

	poolCfg, err := pgxpool.ParseConfig(sourceURL)
	if err != nil {
		return fmt.Errorf("parse source URL: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("open PG source: %w", err)
	}
	defer pool.Close()

	var rawTarget *sql.DB
	if *verifyOnly {
		rawTarget, err = sqlitedb.OpenExisting(ctx, *target, sqlitedb.ExpectedSchemaVersion)
		if err != nil {
			return fmt.Errorf("open existing SQLite target: %w", err)
		}
	} else {
		if err = os.MkdirAll(filepath.Dir(*target), 0o750); err != nil {
			return fmt.Errorf("create target directory: %w", err)
		}
		rawTarget, err = sqlitedb.Open(ctx, *target)
		if err != nil {
			return fmt.Errorf("open SQLite target: %w", err)
		}
		if *migrationsDir != "" {
			err = sqlitedb.Migrate(ctx, rawTarget, os.DirFS(*migrationsDir), ".")
		} else {
			err = sqlitedb.Migrate(ctx, rawTarget, sqlmigrations.FS, ".")
		}
		if err != nil {
			_ = rawTarget.Close()
			return fmt.Errorf("migrate target SQLite schema: %w", err)
		}
	}
	defer func() { _ = rawTarget.Close() }()

	src := cliPGSource{pool: pool}
	tgt := &datamigrate.Target{DB: rawTarget}
	cfg := datamigrate.Config{
		BatchSize:  *batchSize,
		SkipTables: []string{"schema_migrations"},
	}

	precheck, err := datamigrate.Precheck(ctx, src, tgt, tm, nil, "")
	if err != nil {
		return fmt.Errorf("precheck: %w", err)
	}
	if reason := precheck.Abort(); reason != "" {
		return fmt.Errorf("precheck aborted: %s", reason)
	}

	var stats datamigrate.Stats
	idempotentRerun := false
	if !*verifyOnly {
		stats, err = datamigrate.Migrate(ctx, src, tgt, tm, cfg)
		if errors.Is(err, datamigrate.ErrAlreadyMigrated) {
			idempotentRerun = true
		} else if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}

	hashes, err := datamigrate.VerifyRowHashes(ctx, src, tgt, tm, cfg)
	if err != nil {
		return fmt.Errorf("verify row hashes: %w", err)
	}
	schemaDiff, err := datamigrate.DiffSchema(ctx, src, tgt, tm)
	if err != nil {
		return fmt.Errorf("diff schema: %w", err)
	}
	indexDiff, err := datamigrate.DiffIndexes(ctx, src, tgt, tm)
	if err != nil {
		return fmt.Errorf("diff indexes: %w", err)
	}
	recallDiffs, err := datamigrate.VerifyRecallMigration(ctx, src, tgt)
	if err != nil {
		return fmt.Errorf("verify recall migration: %w", err)
	}

	goldenQueries := 0
	goldenZeroDiff := true
	var firstTenantID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM tenants ORDER BY id LIMIT 1").Scan(&firstTenantID); err == nil {
		goldenRep, gErr := datamigrate.VerifyGoldenQueries(ctx, src, tgt, tm, datamigrate.DefaultGoldenQueries(firstTenantID))
		if gErr != nil {
			return fmt.Errorf("verify golden queries: %w", gErr)
		}
		goldenQueries = goldenRep.GoldenQueries
		goldenZeroDiff = goldenRep.GoldenZeroDiff
	}

	var fkViolations int
	if err := rawTarget.QueryRowContext(ctx, "PRAGMA foreign_key_check").Scan(&fkViolations); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	var integrity string
	if err := rawTarget.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}

	zeroDiff := hashes.RowHashZeroDiff &&
		len(schemaDiff) == 0 &&
		len(indexDiff) == 0 &&
		len(recallDiffs) == 0 &&
		goldenZeroDiff &&
		fkViolations == 0 &&
		integrity == "ok"

	mode := "import"
	if *verifyOnly {
		mode = "verify-only"
	}
	report := sqliteImportReport{
		Mode:              mode,
		Target:            *target,
		IdempotentRerun:   idempotentRerun,
		TablesMigrated:    stats.Tables,
		RowsMigrated:      stats.Rows,
		SequencesReset:    stats.SequencesRese,
		TablesCompared:    hashes.TablesCompared,
		RowsCompared:      hashes.RowsCompared,
		RowHashZeroDiff:   hashes.RowHashZeroDiff,
		RowHashDiffs:      hashes.SchemaDiff,
		SchemaDiffCount:   len(schemaDiff),
		IndexDiffCount:    len(indexDiff),
		RecallDiffCount:   len(recallDiffs),
		GoldenQueries:     goldenQueries,
		GoldenZeroDiff:    goldenZeroDiff,
		ForeignKeyIssues:  fkViolations,
		IntegrityCheck:    integrity,
		ZeroDiff:          zeroDiff,
		NOCASEConflicts:   precheck.NOCASEConflicts,
		NumericUsageCount: len(precheck.NumericUsage),
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(output, string(encoded))
	if !zeroDiff {
		return fmt.Errorf("%s failed 0-diff verification", name)
	}
	return nil
}
