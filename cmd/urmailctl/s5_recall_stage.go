package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"

	"github.com/bushixiucai/urmail/internal/datamigrate"
	sqlmigrations "github.com/bushixiucai/urmail/migrations_sqlite"
)

// runS5RecallStage is the controlled, product-level entry for the S5 recall
// offline preservation import. It reads the source URL from an environment
// variable (never argv), builds a private staging SQLite file, verifies the
// recall ledger and schema gates, and publishes the file with a no-overwrite
// link only when every gate passes.
func runS5RecallStage(ctx context.Context, args []string) error {
	return importRecallStage(ctx, args, os.Stdout)
}

func importRecallStage(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("s5-recall-stage", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sourceEnv := flags.String("source-env", "URMAIL_S5_SOURCE_URL", "environment variable holding the migration-role PostgreSQL URL")
	target := flags.String("target", "", "final SQLite path; must not exist")
	migrationsDir := flags.String("migrations-dir", "", "explicit migrations_sqlite directory (default: embedded chain)")
	batchSize := flags.Int("batch-size", 0, "keyset batch size (default 500)")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return errors.New("usage: urmailctl s5-recall-stage --target PATH [--source-env NAME] [--migrations-dir DIR] [--batch-size N]")
	}
	if *target == "" {
		return errors.New("s5-recall-stage requires --target PATH")
	}
	if !filepath.IsAbs(*target) {
		return errors.New("s5-recall-stage requires an absolute --target path")
	}
	if *sourceEnv == "" {
		return errors.New("s5-recall-stage requires a non-empty --source-env name")
	}
	sourceURL := os.Getenv(*sourceEnv)
	if sourceURL == "" {
		return errors.New("s5-recall-stage: the source URL environment variable is empty")
	}
	if err := datamigrate.ValidateStagingSourceURL(sourceURL); err != nil {
		return err
	}

	opts := datamigrate.StagingOptions{
		SourceURL:  sourceURL,
		TargetPath: *target,
		BatchSize:  *batchSize,
	}
	if *migrationsDir != "" {
		tm, err := datamigrate.LoadTypeMap(filepath.Join(*migrationsDir, "type-map.tsv"))
		if err != nil {
			return err
		}
		opts.Migrations = os.DirFS(*migrationsDir)
		opts.MigrationsRoot = "."
		opts.TypeMap = tm
	} else {
		tm, err := datamigrate.ParseTypeMap(bytes.NewReader(sqlmigrations.TypeMapTSV), "migrations_sqlite/type-map.tsv")
		if err != nil {
			return err
		}
		opts.Migrations = sqlmigrations.FS
		opts.MigrationsRoot = "."
		opts.TypeMap = tm
	}

	report, err := datamigrate.ImportRecallStaging(ctx, opts)
	if err != nil {
		return err
	}
	payload := struct {
		Status            string           `json:"status"`
		Target            string           `json:"target"`
		Rows              int              `json:"rows"`
		RecallMigrated    int              `json:"recall_migrated"`
		RecallStatus      map[string]int64 `json:"recall_status"`
		RecallTableHash   string           `json:"recall_table_hash"`
		RecallGuardHash   string           `json:"recall_guard_hash"`
		SchemaDiff        int              `json:"schema_diff"`
		IndexDiff         int              `json:"index_diff"`
		ForeignKeyIssues  int              `json:"foreign_key_issues"`
		Integrity         string           `json:"integrity"`
		RuntimeGates      int              `json:"runtime_gates"`
		RuntimeSkipped    string           `json:"runtime_skipped,omitempty"`
		SourceFingerprint string           `json:"source_fingerprint"`
		SnapshotIsolation string           `json:"snapshot_isolation"`
		SnapshotReadOnly  string           `json:"snapshot_read_only"`
	}{
		Status:            "published",
		Target:            report.TargetPath,
		Rows:              report.Rows,
		RecallMigrated:    report.RecallMigrated,
		RecallStatus:      report.RecallStatus,
		RecallTableHash:   report.RecallTableHash,
		RecallGuardHash:   report.RecallGuardHash,
		SchemaDiff:        report.SchemaDiff,
		IndexDiff:         report.IndexDiff,
		ForeignKeyIssues:  report.ForeignKeyIssues,
		Integrity:         report.Integrity,
		RuntimeGates:      report.RuntimeGates,
		RuntimeSkipped:    report.RuntimeSkipped,
		SourceFingerprint: report.SourceFingerprint,
		SnapshotIsolation: report.SnapshotIsolation,
		SnapshotReadOnly:  report.SnapshotReadOnly,
	}
	return json.NewEncoder(output).Encode(payload)
}
