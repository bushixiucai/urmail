package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/mail"
	"github.com/bushixiucai/urmail/internal/mail/dovecot"
	"github.com/bushixiucai/urmail/internal/mail/nginx"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

func runConfig(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("config requires a subcommand: render")
	}

	switch args[0] {
	case "render":
		return runConfigRender(ctx, args[1:])
	default:
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

func runConfigRender(ctx context.Context, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	outDir := ""
	dryRun := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--out-dir":
			if i+1 < len(args) {
				outDir = args[i+1]
				i++
			}
		}
	}

	hostname := cfg.MyHostname
	if hostname == "" {
		hostname = "box." + cfg.PrimaryDomain
	}

	storageRoot := cfg.StorageRoot
	if storageRoot == "" {
		storageRoot = "/var/mail"
	}

	mailCtx := mail.ConfigContext{
		PostfixNode: postfix.NodeConfig{
			Hostname:       hostname,
			PrimaryDomain:  cfg.PrimaryDomain,
			StorageRoot:    storageRoot,
			TLSCertFile:    filepath.Join(storageRoot, "ssl", hostname+".cert"),
			TLSKeyFile:     filepath.Join(storageRoot, "ssl", hostname+".key"),
			PolicyEnabled:  cfg.PolicyEnabled,
			ArchiveEnabled: cfg.ArchiveEnabled,
		},
		DovecotNode: dovecot.NodeConfig{
			Hostname:      hostname,
			PrimaryDomain: cfg.PrimaryDomain,
			StorageRoot:   storageRoot,
			TLSCertFile:   filepath.Join(storageRoot, "ssl", hostname+".cert"),
			TLSKeyFile:    filepath.Join(storageRoot, "ssl", hostname+".key"),
			DatabaseURL:   cfg.DatabaseURL,
		},
		NSDNode: nsd.NodeConfig{
			Hostname:      hostname,
			PrimaryDomain: cfg.PrimaryDomain,
			PublicIPv4:    "127.0.0.1",
			Nameservers:   []string{"ns1." + cfg.PrimaryDomain},
		},
		NginxNode: nginx.NodeConfig{
			PrimaryDomain: cfg.PrimaryDomain,
			StorageRoot:   storageRoot,
			BackendHTTP:   cfg.ListenAddr,
		},
	}

	files, err := mail.RenderAll(mailCtx)
	if err != nil {
		return fmt.Errorf("render all configs: %w", err)
	}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	if outDir != "" {
		for _, p := range paths {
			target := filepath.Join(outDir, p)
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			if err := os.WriteFile(target, []byte(files[p]), 0644); err != nil {
				return fmt.Errorf("write %s: %w", target, err)
			}
			fmt.Printf("wrote %s\n", target)
		}
		return nil
	}

	// Dry run or stdout display
	for _, p := range paths {
		fmt.Printf("=== %s ===\n", p)
		if !dryRun {
			fmt.Print(files[p])
		}
	}
	return nil
}
