package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"

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
	case "prepare":
		return runConfigPrepare(ctx, args[1:])
	case "status":
		return runConfigStatus(ctx, args[1:])
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
		assembled, err := reconcile.AssembleConfigContext(ctx, cfg, &reconcile.VersionedStore{StoreQuerier: store.New(pool.Pool), DB: pool.Pool}, cfg.PublicIPv4, cfg.Nameservers)
		if err != nil {
			return fmt.Errorf("assemble config context: %w", err)
		}
		mailCtx = *assembled
	} else {
		mailCtx = mail.ConfigContext{
			PostfixNode: postfix.NodeConfig{
				Hostname:                 hostname,
				PrimaryDomain:            cfg.PrimaryDomain,
				StorageRoot:              storageRoot,
				TLSCertFile:              filepath.Join("/var/lib/urmail-agent/certs/mail", hostname+".cert"),
				TLSKeyFile:               filepath.Join("/var/lib/urmail-agent/certs/mail", hostname+".key"),
				PolicyEnabled:            cfg.PolicyEnabled,
				V80Preserve:              cfg.PostfixV80Preserve,
				PreserveExternalOpenDKIM: cfg.PreserveExternalOpenDKIM,
				ArchiveEnabled:           cfg.ArchiveEnabled,
			},
			DovecotNode: dovecot.NodeConfig{
				Hostname:      hostname,
				PrimaryDomain: cfg.PrimaryDomain,
				StorageRoot:   storageRoot,
				TLSCertFile:   filepath.Join("/var/lib/urmail-agent/certs/mail", hostname+".cert"),
				TLSKeyFile:    filepath.Join("/var/lib/urmail-agent/certs/mail", hostname+".key"),
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
		if err := mail.ExportFiles(outDir, files); err != nil {
			return fmt.Errorf("export configs: %w", err)
		}
		for _, p := range paths {
			fmt.Printf("wrote %s\n", filepath.Join(outDir, p))
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

func runConfigStatus(ctx context.Context, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	wait := time.Duration(0)
	if len(args) > 0 {
		if len(args) != 2 || args[0] != "--wait" {
			return fmt.Errorf("usage: config status [--wait duration]")
		}
		wait, err = time.ParseDuration(args[1])
		if err != nil {
			return err
		}
	}
	deadline := time.Now().Add(wait)
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		resp, err := agent.NewClient(cfg.AgentSocket).Do(attemptCtx, agentd.OpConfigStatus, struct{}{})
		cancel()
		if err == nil {
			var status agentd.ApplyStatus
			if err = json.Unmarshal([]byte(resp.Stdout), &status); err == nil {
				if status.Ready && !status.Degraded && status.Desired == status.Applied && status.Applied != "" {
					fmt.Println(resp.Stdout)
					return nil
				}
				if wait == 0 || time.Now().After(deadline) {
					fmt.Println(resp.Stdout)
					return fmt.Errorf("configuration is not ready: %s", status.Step)
				}
			}
		}
		if wait == 0 || time.Now().After(deadline) {
			return fmt.Errorf("configuration readiness timed out: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
