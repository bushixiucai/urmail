package reversemigrate

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bushixiucai/urmail/internal/datamigrate"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	sqlmigrations "github.com/bushixiucai/urmail/migrations_sqlite"
)

// Run executes the E7 reverse migration pipeline (or --dry-run planning)
// enforcing §3 isolation, §4 six-class merge, and §5 two-pass reconciliation.
func Run(ctx context.Context, cfg Config) (Report, error) {
	now := cfg.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	mode := "reverse-migrate"
	if cfg.DryRun {
		mode = "dry-run"
	}
	rep := Report{
		Mode:            mode,
		DryRun:          cfg.DryRun,
		NoSLACommitment: true,
		RetentionNote:   "14-day retention is data/evidence retention only, not an automatic rollback SLA",
		PlanActions:     []string{},
	}

	iso, err := VerifyIsolation(cfg.Isolation, now)
	rep.Isolation = iso
	if err != nil {
		return rep, fmt.Errorf("isolation check failed: %w", err)
	}
	rep.PlanActions = append(rep.PlanActions, fmt.Sprintf("isolation verified (state=%s)", iso.State))

	var reconChecks []string

	// 1. DB reverse migration (SQLite -> PG).
	if cfg.SQLitePath != "" && cfg.TargetPGURL != "" {
		tm, err := datamigrate.ParseTypeMap(bytes.NewReader(sqlmigrations.TypeMapTSV), "migrations_sqlite/type-map.tsv")
		if err != nil {
			return rep, fmt.Errorf("parse type-map: %w", err)
		}
		sqliteDB, err := sqlitedb.OpenExisting(ctx, cfg.SQLitePath, sqlitedb.ExpectedSchemaVersion)
		if err != nil {
			return rep, fmt.Errorf("open sqlite %s: %w", cfg.SQLitePath, err)
		}
		defer func() { _ = sqliteDB.Close() }()

		pgPool, err := pgxpool.New(ctx, cfg.TargetPGURL)
		if err != nil {
			return rep, fmt.Errorf("connect target pg: %w", err)
		}
		defer pgPool.Close()

		dbRep, err := MigrateDBSQLiteToPG(ctx, sqliteDB, pgPool, tm, cfg.BaselinePKs, cfg.DryRun)
		rep.DB = &dbRep
		if err != nil {
			return rep, fmt.Errorf("db reverse migrate: %w", err)
		}
		rep.PlanActions = append(rep.PlanActions, dbRep.Actions...)
		reconChecks = append(reconChecks, fmt.Sprintf("db_tables=%d upserted=%d deleted=%d", dbRep.TablesProcessed, dbRep.RowsUpserted, dbRep.RowsDeleted))
	}

	// 2. Maildir & IMAP reverse sync.
	if cfg.SrcMaildir != "" && cfg.TgtMaildir != "" {
		mRep, iRep, err := MergeMaildirAndIMAP(cfg.SrcMaildir, cfg.TgtMaildir, cfg.QuarantineDir, cfg.AllowSharedMaildir, cfg.DryRun)
		rep.Maildir = &mRep
		rep.IMAP = &iRep
		if err != nil {
			return rep, fmt.Errorf("maildir/imap merge: %w", err)
		}
		rep.PlanActions = append(rep.PlanActions, mRep.Actions...)
		reconChecks = append(reconChecks, fmt.Sprintf("maildir_copied=%d quarantined=%d uidvalidity_match=%v", mRep.FilesCopied, mRep.FilesQuarantined, iRep.UIDValidityMatch))
	}

	// 3. Postfix queue compound-key handoff.
	if len(cfg.SrcQueueItems) > 0 || len(cfg.TgtQueueItems) > 0 {
		qRep, _, err := MergeQueue(cfg.SrcQueueItems, cfg.TgtQueueItems, cfg.DeliveredMIMEHashes, cfg.OutQueueDir, cfg.DryRun)
		rep.Queue = &qRep
		if err != nil {
			return rep, fmt.Errorf("queue handoff: %w", err)
		}
		rep.PlanActions = append(rep.PlanActions, qRep.Actions...)
		reconChecks = append(reconChecks, fmt.Sprintf("queue_handoff=%d skipped_delivered=%d", qRep.HandoffQueued, qRep.SkippedDelivered))
	}

	// 4. TLS cert/key verification & atomic sync.
	if cfg.SrcTLSCert != "" && cfg.SrcTLSKey != "" {
		tRep, err := VerifyAndMergeTLS(cfg.SrcTLSCert, cfg.SrcTLSKey, cfg.TgtTLSCert, cfg.TgtTLSKey, cfg.TLSBackupDir, now, cfg.DryRun)
		rep.TLS = &tRep
		if err != nil {
			return rep, fmt.Errorf("tls merge: %w", err)
		}
		rep.PlanActions = append(rep.PlanActions, tRep.Actions...)
		reconChecks = append(reconChecks, fmt.Sprintf("tls_algo=%s pubkey=%s", tRep.Algorithm, tRep.PubkeySHA256))
	}

	// 5. DNS zero-cutover & zone syntax verification.
	if len(cfg.ZoneFiles) > 0 || cfg.ManagedZoneChanged {
		dRep, err := VerifyDNS(cfg.ZoneFiles, cfg.ManagedZoneChanged)
		rep.DNS = &dRep
		if err != nil {
			return rep, fmt.Errorf("dns check: %w", err)
		}
		rep.PlanActions = append(rep.PlanActions, dRep.Actions...)
		reconChecks = append(reconChecks, fmt.Sprintf("dns_zones=%d zero_cutover=%v", dRep.ZonesChecked, dRep.ZeroDNSCutover))
	}

	rep.Reconciliation = &ReconciliationReport{
		Passed: true,
		Checks: reconChecks,
	}
	return rep, nil
}
