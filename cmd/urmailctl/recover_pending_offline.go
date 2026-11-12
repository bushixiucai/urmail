package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/bushixiucai/urmail/internal/agentd"
)

// runRecoverPendingOffline exposes the explicit fenced recovery path. All
// authorization, lock, receipt, writer-state and no-service-side-effect checks
// live in the agentd operation, which owns operation.lock exactly once.
func runRecoverPendingOffline(ctx context.Context, args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("recover-pending requires root")
	}
	fs := flag.NewFlagSet("recover-pending", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: urmailctl recover-pending")
	}
	backupDir, err := agentd.RecoverPendingJournalOffline(ctx, currentMaintenanceRoot())
	if err != nil {
		return err
	}
	fmt.Printf("PENDING_RECOVERY=completed private_backup=%s\n", backupDir)
	return nil
}
