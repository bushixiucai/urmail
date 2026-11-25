package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/usagesync"
)

func parseQuotaStorage(raw []byte) (int64, error) {
	return usagesync.ParseQuotaStorage(raw)
}

func parseMaildirQuotaBytes(r io.Reader) (int64, error) {
	return usagesync.ParseMaildirQuotaBytes(r)
}

func runUsageSync(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "sync" {
		return fmt.Errorf("usage requires sync")
	}
	if backupWritersHeld() {
		return errors.New("usage sync refused during restore or upgrade hold")
	}
	lock, err := backupLock(currentMaintenanceRoot(), "operation.lock")
	if err != nil {
		return fmt.Errorf("usage sync lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if backupWritersHeld() {
		return errors.New("usage sync refused during restore or upgrade hold")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()

	count, err := usagesync.Sync(ctx, usagesync.Options{
		Pool:          pool,
		StorageRoot:   cfg.StorageRoot,
		SkipHoldCheck: true, // hold & lock already verified above
	})
	if err != nil {
		return err
	}
	fmt.Printf("storage measurements updated for %d mailboxes\n", count)
	return nil
}
