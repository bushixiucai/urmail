package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/bushixiucai/urmail/internal/config"
)

func runRestore(ctx context.Context, args []string) error {
	dumpPath, configPath, databaseURL := "", "", ""
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
		databaseURL = cfg.DatabaseURLPlatform
	}
	cmd := exec.CommandContext(ctx, "pg_restore", "--clean", "--if-exists", "--no-owner", "--no-acl", "--dbname", databaseURL, dumpPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore: %w", err)
	}
	if configPath != "" {
		cmd = exec.CommandContext(ctx, "tar", "-xzf", configPath, "-C", "/")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("restore config: %w", err)
		}
	}
	fmt.Println("restore complete")
	return nil
}
