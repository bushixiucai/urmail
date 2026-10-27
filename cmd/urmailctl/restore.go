package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	domainRestore "github.com/bushixiucai/urmail/internal/domain/restore"
)

func runRestore(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "mailbox" {
		return runMailboxRestore(ctx, args[1:])
	}
	if _, selected, err := selectedFileProfile(args); err != nil {
		return err
	} else if selected {
		return runV80Restore(ctx, args, os.Stdout)
	}
	if len(args) > 0 && args[0] == "quarantine" {
		return runRestoreQuarantine(ctx, args[1:])
	}
	dumpPath, configPath, databaseURL := "", "", ""
	mailPath, manifestPath, resumeID := "", "", ""
	force := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dump":
			if i+1 < len(args) {
				dumpPath = args[i+1]
				i++
			}
		case "--config":
			if i+1 < len(args) {
				configPath = args[i+1]
				i++
			}
		case "--mail":
			if i+1 < len(args) {
				mailPath = args[i+1]
				i++
			}
		case "--manifest":
			if i+1 < len(args) {
				manifestPath = args[i+1]
				i++
			}
		case "--database-url":
			if i+1 < len(args) {
				databaseURL = args[i+1]
				i++
			}
		case "--resume-id":
			if i+1 < len(args) {
				i++
				resumeID = args[i]
			}
		case "--force":
			force = true
		default:
			return fmt.Errorf("unknown restore argument %q", args[i])
		}
	}
	if dumpPath == "" || !force {
		return fmt.Errorf("restore requires --dump FILE and --force (it overwrites the target database)")
	}
	if manifestPath == "" || (mailPath != "" && configPath == "") {
		return fmt.Errorf("restore requires --manifest")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("restore must run as root")
	}
	if err := rejectV80BundleAsDatabase(dumpPath); err != nil {
		return err
	}
	var resumedURLs map[string]string
	if resumeID != "" {
		var err error
		resumedURLs, err = restoreResumeCredentials(config.RestoreQuarantineMarker, resumeID)
		if err != nil {
			return err
		}
		if databaseURL == "" && len(resumedURLs) > 0 {
			databaseURL = resumedURLs["URMAIL_DATABASE_URL_MIGRATE"]
		}
	}
	if databaseURL == "" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		databaseURL = cfg.DatabaseURLMigrate
		if databaseURL == "" {
			return fmt.Errorf("restore requires the migration database role")
		}
	}
	if manifestPath == "" || (mailPath != "" && configPath == "") {
		return fmt.Errorf("restore requires --manifest")
	}
	if err := verifyBackupManifest(manifestPath, []string{dumpPath, configPath, mailPath}, mailPath != ""); err != nil {
		return err
	}
	var targetURLs map[string]string
	if configPath != "" {
		var err error
		if resumeID != "" && len(resumedURLs) > 0 {
			targetURLs = resumedURLs
		} else {
			target, loadErr := config.Load()
			if loadErr != nil {
				return fmt.Errorf("load target role credentials before restoring config: %w", loadErr)
			}
			targetURLs, err = targetDatabaseURLs(target, databaseURL)
			if err != nil {
				return err
			}
		}
		targetURLs["URMAIL_DATABASE_URL_MIGRATE"] = databaseURL
		if err = verifyTargetRoleConnections(ctx, targetURLs); err != nil {
			return err
		}
	}
	for _, archive := range []string{configPath, mailPath} {
		if archive != "" {
			if err := validateRestoreArchive(archive); err != nil {
				return err
			}
		}
	}
	fingerprint, err := restoreInputFingerprint(databaseURL, []string{dumpPath, configPath, mailPath, manifestPath})
	if err != nil {
		return err
	}
	var state restoreQuarantineState
	databaseRestored := false
	if resumeID != "" {
		state, databaseRestored, err = resumeRestoreQuarantine(config.RestoreQuarantineMarker, resumeID, fingerprint)
	} else {
		state, err = createRestoreQuarantine(config.RestoreQuarantineMarker)
		if err == nil {
			err = writeRestoreProof(config.RestoreQuarantineMarker, "input.json", restoreInputProof{state.ID, fingerprint})
		}
		if err == nil && len(targetURLs) > 0 {
			err = writeRestoreProof(config.RestoreQuarantineMarker, "target-credentials.json", targetURLs)
		}
	}
	if err != nil {
		return err
	}
	if resumeID != "" && configPath != "" && len(resumedURLs) == 0 {
		if databaseRestored {
			return fmt.Errorf("restore target credential proof is missing; quarantine retained")
		}
		if err = writeRestoreProof(config.RestoreQuarantineMarker, "target-credentials.json", targetURLs); err != nil {
			return err
		}
	}
	operationLock, err := acquireRestoreOperation(config.RestoreQuarantineMarker)
	if err != nil {
		return err
	}
	defer func() { _ = operationLock.Close() }()
	fmt.Printf("restore quarantine id=%s resumed=%t; retained on every exit\n", state.ID, resumeID != "")
	if err = stopRestoreWriters(ctx, stopSystemdRestoreWriter); err != nil {
		return err
	}
	var cmd *exec.Cmd
	if !databaseRestored {
		err = domainRestore.BeforeOverwrite(ctx, func(ctx context.Context) error {
			pool, openErr := db.OpenPlatform(ctx, databaseURL)
			if openErr != nil {
				return openErr
			}
			defer pool.Close()
			tx, txErr := pool.PlatformTx(ctx)
			if txErr != nil {
				return txErr
			}
			defer func() { _ = tx.Rollback(ctx) }()
			var active bool
			if scanErr := tx.QueryRow(ctx, "SELECT urmail_begin_restore_quarantine()").Scan(&active); scanErr != nil || !active {
				return domainRestore.ErrQuarantine
			}
			return tx.Commit(ctx)
		}, func(ctx context.Context) error {
			return restoreDatabaseArchive(ctx, databaseURL, dumpPath, config.RestoreQuarantineMarker, operationLock)
		})
		if err != nil {
			return err
		}
		if err = writeRestoreProof(config.RestoreQuarantineMarker, "database-restored.json", restoreInputProof{state.ID, fingerprint}); err != nil {
			return err
		}
	}
	if err := db.FinalizeRestore(ctx, databaseURL, state.ID); err != nil {
		return err
	}
	migrator, err := db.NewMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = migrator.Close() }()
	if err = migrator.Up(ctx); err != nil {
		return err
	}
	if err = db.HoldRestoredSideEffects(ctx, databaseURL, state.ID); err != nil {
		return err
	}
	if err = db.VerifyRuntimePrivileges(ctx, databaseURL); err != nil {
		return err
	}

	for _, archive := range []string{configPath, mailPath} {
		if archive != "" {
			cmd = exec.CommandContext(ctx, "tar", "-xzf", archive, "-C", "/") // #nosec G204 G702 -- fixed executable; root-authorized file/URL arguments are passed separately without a shell.
			cmd.ExtraFiles = append(cmd.ExtraFiles, operationLock)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("restore config: %w", err)
			}
		}
	}
	if configPath != "" {
		if err = preserveTargetDatabaseURLs(targetURLs); err != nil {
			return err
		}
	}

	if configPath != "" {
		if err = configureRestoredRoundcube(ctx, operationLock); err != nil {
			return err
		}
	}

	if err = completeRestoreQuarantine(config.RestoreQuarantineMarker, state.ID); err != nil {
		return err
	}
	pool, err := db.OpenPlatform(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var released bool
	if err = tx.QueryRow(ctx, "SELECT urmail_release_restore_quarantine()").Scan(&released); err != nil || !released {
		return fmt.Errorf("restore quarantine remains active")
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Printf("restore complete (mail_contents=%t); QUARANTINED id=%s; writers remain stopped. Reconcile archive spool, marketing claims, queue, financial state and telemetry source before explicit release.\n", mailPath != "", state.ID)
	return nil
}
