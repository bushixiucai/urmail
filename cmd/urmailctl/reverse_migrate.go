package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/reversemigrate"
)

func runCutoverReverseMigrate(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("cutover reverse-migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	dryRun := flags.Bool("dry-run", false, "emit planned reverse-migration actions without writing target state")
	holdFile := flags.String("hold-file", "/var/lib/urmail-backup/upgrade.hold", "upgrade hold file path")
	expectedGen := flags.String("expected-generation", "", "expected hold generation token")
	listenersStopped := flags.Bool("listeners-stopped", false, "attest all inbound/HTTP listeners are stopped")
	writersStopped := flags.Bool("writers-stopped", false, "attest all Compose/systemd writers are stopped")
	walClean := flags.Bool("sqlite-wal-clean", false, "attest SQLite WAL has been checkpointed and frozen")
	pgWritersZero := flags.Bool("pg-writers-zero", false, "attest zero active PG business writers")

	sqlitePath := flags.String("sqlite", "", "frozen source SQLite database path")
	pgEnv := flags.String("target-pg-env", "URMAIL_REVERSE_TARGET_PG_URL", "environment variable holding target PostgreSQL URL")
	srcMaildir := flags.String("src-maildir", "", "frozen source Maildir root")
	tgtMaildir := flags.String("tgt-maildir", "", "target Maildir root")
	quarantineDir := flags.String("quarantine-dir", "", "quarantine directory for Maildir files deleted on source")
	allowShared := flags.Bool("allow-shared-maildir-verified", false, "confirm pre-cutover & pre-merge readonly backups exist for shared Maildir")

	srcQueueFile := flags.String("src-queue-json", "", "JSON file listing frozen source Postfix queue items")
	tgtQueueFile := flags.String("tgt-queue-json", "", "JSON file listing target Postfix queue items")
	outQueueDir := flags.String("out-queue-dir", "", "output directory for deduplicated queue handoff manifest")

	srcCert := flags.String("src-tls-cert", "", "source TLS certificate PEM path")
	srcKey := flags.String("src-tls-key", "", "source TLS private key PEM path")
	tgtCert := flags.String("tgt-tls-cert", "", "target TLS certificate PEM path")
	tgtKey := flags.String("tgt-tls-key", "", "target TLS private key PEM path")
	tlsBackupDir := flags.String("tls-backup-dir", "", "backup directory before replacing target TLS cert/key")

	zonesCSV := flags.String("zone-files", "", "comma-separated managed NSD zone files to syntax-verify")
	managedDNSChanged := flags.Bool("managed-dns-changed", false, "assert managed DNS zones changed and must be verified")

	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: urmailctl cutover reverse-migrate [--dry-run] --hold-file PATH --listeners-stopped --writers-stopped --sqlite-wal-clean --pg-writers-zero")
	}

	cfg := reversemigrate.Config{
		DryRun: *dryRun,
		Isolation: reversemigrate.IsolationCheckInput{
			HoldFilePath:        *holdFile,
			ExpectedGeneration:  *expectedGen,
			ListenersStopped:    listenersStopped,
			WritersStopped:      writersStopped,
			SQLiteWALClean:      walClean,
			PGActiveWritersZero: pgWritersZero,
		},
		SQLitePath:         *sqlitePath,
		TargetPGURL:        os.Getenv(*pgEnv),
		SrcMaildir:         *srcMaildir,
		TgtMaildir:         *tgtMaildir,
		QuarantineDir:      *quarantineDir,
		AllowSharedMaildir: *allowShared,
		OutQueueDir:        *outQueueDir,
		SrcTLSCert:         *srcCert,
		SrcTLSKey:          *srcKey,
		TgtTLSCert:         *tgtCert,
		TgtTLSKey:          *tgtKey,
		TLSBackupDir:       *tlsBackupDir,
		ManagedZoneChanged: *managedDNSChanged,
		Now:                time.Now().UTC(),
	}

	if *srcQueueFile != "" {
		items, err := loadQueueItemsJSON(*srcQueueFile)
		if err != nil {
			return fmt.Errorf("read src-queue-json: %w", err)
		}
		cfg.SrcQueueItems = items
	}
	if *tgtQueueFile != "" {
		items, err := loadQueueItemsJSON(*tgtQueueFile)
		if err != nil {
			return fmt.Errorf("read tgt-queue-json: %w", err)
		}
		cfg.TgtQueueItems = items
	}
	if strings.TrimSpace(*zonesCSV) != "" {
		for _, p := range strings.Split(*zonesCSV, ",") {
			if tr := strings.TrimSpace(p); tr != "" {
				cfg.ZoneFiles = append(cfg.ZoneFiles, tr)
			}
		}
	}

	rep, runErr := reversemigrate.Run(ctx, cfg)
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if encErr := enc.Encode(rep); encErr != nil {
		return encErr
	}
	return runErr
}

func loadQueueItemsJSON(path string) ([]reversemigrate.QueueItem, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied queue JSON path
	if err != nil {
		return nil, err
	}
	var items []reversemigrate.QueueItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}
