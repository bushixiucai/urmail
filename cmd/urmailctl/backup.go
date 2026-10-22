package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
)

func runBackup(ctx context.Context, args []string) error {
	outDir := "/var/lib/urmail/backups"
	for i := 0; i < len(args); i++ {
		if args[i] == "--out-dir" && i+1 < len(args) {
			outDir = args[i+1]
			i++
		}
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("backup must run as root")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// The platform role owns the tables and has BYPASSRLS, which pg_dump
	// needs to read audit/tenant rows.
	dbURL := cfg.DatabaseURLPlatform
	if dbURL == "" {
		dbURL = cfg.DatabaseURLMigrate
	}
	if dbURL == "" {
		return fmt.Errorf("no database URL configured")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	dump := filepath.Join(outDir, "urmail-"+stamp+".dump")
	cmd := exec.CommandContext(ctx, "pg_dump", "--no-owner", "--format=custom", "--file="+dump, dbURL)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %w", err)
	}
	if err := os.Chmod(dump, 0o600); err != nil {
		return err
	}
	configArchive := filepath.Join(outDir, "urmail-config-"+stamp+".tar.gz")
	cmd = exec.CommandContext(ctx, "tar", "-czf", configArchive,
		"-C", "/",
		"etc/urmail",
		"etc/postfix/urmail",
		"etc/opendkim",
		"var/lib/urmail/certs",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tar config: %w", err)
	}
	if err := os.Chmod(configArchive, 0o600); err != nil {
		return err
	}
	manifest := filepath.Join(outDir, "urmail-"+stamp+".sha256")
	if err := writeChecksums(manifest, []string{dump, configArchive}); err != nil {
		return err
	}
	fmt.Printf("backup complete\n  database: %s\n  config:   %s\n  manifest: %s\n", dump, configArchive, manifest)
	return nil
}

func writeChecksums(path string, files []string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, name := range files {
		h, err := fileSHA256(name)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(f, "%s  %s\n", h, filepath.Base(name)); err != nil {
			return err
		}
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
