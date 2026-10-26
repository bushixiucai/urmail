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
                  [--free-until RFC3339 --free-reason TEXT]
  urmailctl status                 print the service status
  urmailctl config render [--dry-run] [--out-dir <dir>]
  urmailctl cert issue <domain>     obtain an ACME certificate via certbot
  urmailctl cert renew              renew ACME certificates and install them
  urmailctl usage sync             collect canonical Dovecot storage usage
  urmailctl billing repair-legacy-period --tenant-id T --subscription-id S --paid-order-id O --expected-start RFC3339 --expected-end RFC3339 --nominal-basis-cents N --reason TEXT [--apply]
  urmailctl import-miadb --dry-run --config FILE --sqlite FILE [--mapping FILE]
  urmailctl import-legacy-core --apply --config FILE --sqlite FILE --mapping FILE --expected-snapshot SHA --tenant-id N --actor-id N --tenant-ref REF --reason TEXT
  urmailctl import-legacy-dkim --apply --config FILE --sqlite FILE --mapping FILE --clone-root DIR --expected-snapshot SHA --expected-private SHA --expected-keytable SHA --expected-signing SHA --tenant-id N --actor-id N --tenant-ref REF --reason TEXT
  urmailctl import-legacy-activate-mail --apply --expected-core-proof SHA --expected-domains N --tenant-id N --actor-id N --tenant-ref REF --reason TEXT
  urmailctl data-check             report tenant reference/allocation problems
  urmailctl telemetry acknowledge-gap --stream NAME --generation N --reason TEXT
  urmailctl secrets migrate-totp [--apply] [--batch-size N]
  urmailctl secrets compact-rejected-webhooks [--apply]
  urmailctl domain challenge --tenant-id T --domain-id D
  urmailctl domain verify --tenant-id T --domain-id D
  urmailctl backup [--out-dir DIR]  dump PostgreSQL and config/certificate files
  urmailctl restore quarantine status|release [--restore-id ID --confirm-reconciled --source-isolated --reason TEXT]
  urmailctl restore --dump FILE --force [--config FILE] [--database-url URL]
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
	case "alias-senders":
		return runAliasSenders(ctx, args[1:])
	case "cert":
		return runCert(ctx, args[1:])
	case "usage":
		return runUsageSync(ctx, args[1:])
	case "telemetry":
		return runTelemetry(ctx, args[1:])
	case "secrets":
		return runSecrets(ctx, args[1:])
	case "domain":
		return runDomainRecovery(ctx, args[1:])
	case "billing":
		return runBillingRepair(ctx, args[1:])
	case "import-miadb":
		return runImportMIADB(ctx, args[1:])
	case "import-legacy-core":
		return runImportLegacyCore(ctx, args[1:])
	case "import-legacy-dkim":
		return runImportLegacyDKIM(ctx, args[1:])
	case "import-legacy-activate-mail":
		return runImportLegacyActivateMail(ctx, args[1:])
	case "data-check":
		return runDataCheck(ctx)
	case "backup":
		return runBackup(ctx, args[1:])
	case "restore":
		return runRestore(ctx, args[1:])
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
