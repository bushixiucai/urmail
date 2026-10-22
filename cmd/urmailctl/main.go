// Command urmailctl is the administrative CLI of urMail.cn.
//
// It covers the operations an operator runs from a shell: schema migration,
// status reporting and maintenance tasks that do not belong in the HTTP API.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
)

const usage = `urmailctl — urMail.cn administrative tool

Usage:
  urmailctl bootstrap [--database-url URL]  create cluster roles (as superuser)
  urmailctl migrate up             apply pending schema migrations
  urmailctl migrate down           roll back the most recent schema migration
  urmailctl migrate version        print the current schema version
  urmailctl setup --email E --password P [--brand B] [--tenant T]
  urmailctl status                 print the service status
  urmailctl config render [--dry-run] [--out-dir <dir>]
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "urmailctl: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "bootstrap":
		return runBootstrap(ctx, args[1:])
	case "migrate":
		return runMigrate(ctx, args[1:])
	case "setup":
		return runSetup(ctx, args[1:])
	case "status":
		return runStatus(ctx)
	case "config":
		return runConfig(ctx, args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runMigrate(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("migrate requires a direction: up, down or version")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	migrateURL := cfg.DatabaseURLMigrate
	if migrateURL == "" {
		// Backward-compatible development fallback; production installs set
		// URMAIL_DATABASE_URL_MIGRATE to the dedicated migration role.
		migrateURL = cfg.DatabaseURLPlatform
	}
	migrator, err := db.NewMigrator(migrateURL)
	if err != nil {
		return err
	}
	defer func() { _ = migrator.Close() }()

	switch args[0] {
	case "up":
		if err := migrator.Up(ctx); err != nil {
			return err
		}
		fmt.Println("migrations applied")
		return nil
	case "down":
		if err := migrator.Down(ctx); err != nil {
			return err
		}
		fmt.Println("migration rolled back")
		return nil
	case "version":
		version, dirty, err := migrator.Version()
		if err != nil {
			return err
		}
		fmt.Printf("schema version %d (dirty=%t)\n", version, dirty)
		return nil
	default:
		return fmt.Errorf("unknown migrate direction %q", args[0])
	}
}

func runStatus(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("database unreachable: %w", err)
	}
	fmt.Println("database: ok")
	fmt.Printf("storage root: %s\n", cfg.StorageRoot)
	return nil
}
