// Command urmail-archive is the Postfix pipe transport that stores a copy of
// each delivered message and records it in archive_entries.
//
// Archive-copy failures defer only the archive transport recipient for retry.
// The original recipient is delivered independently; no durable copy is acknowledged before persistence.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	archiveStore "github.com/bushixiucai/urmail/internal/archive"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

func main() {
	fs := flag.NewFlagSet("archive-pipe", flag.ContinueOnError)
	recipient := fs.String("recipient", "", "Postfix envelope recipient")
	envFile := fs.String("env-file", "/etc/urmail/urmaild.env", "urmaild environment file")
	if err := fs.Parse(os.Args[1:]); err != nil {
		log.Printf("archive-pipe: %v", err)
		os.Exit(75)
	}
	if *recipient == "" {
		log.Printf("archive-pipe: empty recipient")
		os.Exit(75)
	}
	if err := run(*recipient, *envFile); err != nil {
		log.Printf("archive-pipe: %v", err)
		os.Exit(75)
	}
}

func run(recipient, envFile string) error {
	binding, err := domainMail.DecodeArchiveRecipient(recipient)
	if err != nil {
		return err
	}
	manifest, err := archiveStore.Spool(archiveStore.SpoolRoot, binding, os.Stdin)
	if err != nil {
		return fmt.Errorf("durable archive spool: %w", err)
	}
	_ = os.Setenv("URMAIL_ENV_FILE", envFile)
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("archive retained in spool; config: %w", err)
	}
	if cfg.Quarantined {
		return fmt.Errorf("archive retained in spool during restore quarantine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return fmt.Errorf("archive retained in spool; database: %w", err)
	}
	defer pool.Close()
	if binding.TenantID == 0 {
		mailbox, resolveErr := store.New(pool.Pool).PlatformGetMailboxByAddress(ctx, binding.Address)
		if resolveErr != nil {
			return fmt.Errorf("legacy archive retained unbound; immediate canonical resolution: %w", resolveErr)
		}
		if err = archiveStore.BindLegacy(archiveStore.SpoolRoot, manifest, mailbox.TenantID, mailbox.ID); err != nil {
			return err
		}
	}
	worker := &archiveStore.Worker{Pool: pool}
	return worker.IndexSpoolCopy(ctx, manifest.ID)
}
