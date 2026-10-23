package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
)

func runRestore(ctx context.Context, args []string) error {
	dumpPath, configPath, databaseURL := "", "", ""
	mailPath, manifestPath := "", ""
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
		case "--force":
			force = true
		}
	}
	if dumpPath == "" || !force {
		return fmt.Errorf("restore requires --dump FILE and --force (it overwrites the target database)")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("restore must run as root")
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
	if mailPath != "" && (manifestPath == "" || configPath == "") {
		return fmt.Errorf("mail restore requires --manifest and --config")
	}
	if manifestPath != "" {
		if err := verifyBackupManifest(manifestPath, []string{dumpPath, configPath, mailPath}, mailPath != ""); err != nil {
			return err
		}
	}
	var targetURLs map[string]string
	if configPath != "" {
		target, err := config.Load()
		if err != nil {
			return fmt.Errorf("load target role credentials before restoring config: %w", err)
		}
		targetURLs, err = targetDatabaseURLs(target, databaseURL)
		if err != nil {
			return err
		}
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
	cmd, err := postgresCommand(ctx, "pg_restore", databaseURL, "--clean", "--if-exists", "--no-owner", "--exit-on-error", "--single-transaction", dumpPath)
	if err != nil {
		return err
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore: %w", err)
	}
	if err := db.FinalizeRestore(ctx, databaseURL); err != nil {
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
	if err = db.VerifyRuntimePrivileges(ctx, databaseURL); err != nil {
		return err
	}

	for _, archive := range []string{configPath, mailPath} {
		if archive != "" {
			cmd = exec.CommandContext(ctx, "tar", "-xzf", archive, "-C", "/") // #nosec G204 G702 -- fixed executable; root-authorized file/URL arguments are passed separately without a shell.
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

	fmt.Printf("restore complete (mail_contents=%t)\n", mailPath != "")
	return nil
}
