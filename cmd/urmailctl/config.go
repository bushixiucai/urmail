package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mail"
	"github.com/bushixiucai/urmail/internal/mail/dovecot"
	"github.com/bushixiucai/urmail/internal/mail/nginx"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/store"
)

// withHTTPScheme prefixes a bare host:port address with http:// unless it
// already carries a scheme (nginx.Generate validates backends with
// ^https?://...).
func withHTTPScheme(addr string) string {
	if addr == "" || strings.Contains(addr, "://") {
		return addr
	}
	return "http://" + addr
}

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
	dovecotDB := cfg.DatabaseURLDovecot
	if dovecotDB == "" {
		dovecotDB = cfg.DatabaseURLPlatform
	}
	if dovecotDB == "" {
		dovecotDB = cfg.DatabaseURL
	}

	// Assemble the full context (node config + tenant snapshot) from the
	// database when a platform connection is configured. Without a database
	// the render falls back to an empty skeleton, which is only useful for
	// plumbing checks.
	var mailCtx mail.ConfigContext
	if cfg.DatabaseURLPlatform != "" {
		pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
		if err != nil {
			return fmt.Errorf("open platform database: %w", err)
		}
		defer pool.Close()
		assembled, err := reconcile.AssembleConfigContext(ctx, cfg, store.New(pool.Pool), cfg.PublicIPv4, cfg.Nameservers)
		if err != nil {
			return fmt.Errorf("assemble config context: %w", err)
		}
		mailCtx = *assembled
	} else {
		mailCtx = mail.ConfigContext{
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
				DatabaseURL:   dovecotDB,
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
				// nginx backends are validated to carry an explicit scheme.
				BackendHTTP: withHTTPScheme(cfg.ListenAddr),
			},
		}
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
			// CONTRACT: db.conf embeds the database URL (credentials) and is
			// always persisted 0600, matching the dovecot render contract and
			// the agentd file.write enforcement (M2 review P1-2).
			mode := os.FileMode(0o644)
			if filepath.Base(target) == "db.conf" {
				mode = 0o600
			}
			if err := os.WriteFile(target, []byte(files[p]), mode); err != nil {
				return fmt.Errorf("write %s: %w", target, err)
			}
			if err := os.Chmod(target, mode); err != nil {
				return fmt.Errorf("chmod %s: %w", target, err)
			}
			fmt.Printf("wrote %s\n", target)
		}
		return nil
	}

	if !dryRun {
		return fmt.Errorf("refusing to print configuration files (db.conf contains credentials); use --out-dir or --dry-run")
	}
	for _, p := range paths {
		fmt.Printf("=== %s ===\n", p)
	}
	return nil
}
