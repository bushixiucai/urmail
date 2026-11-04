package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/maintenance"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/store"
)

// runConfigPrepare is deliberately offline: no daemon or protocol service is started.
func runConfigPrepare(ctx context.Context, args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("config prepare requires root")
	}
	if len(args) != 0 {
		return errors.New("usage: config prepare")
	}
	root := currentMaintenanceRoot()
	lock, err := maintenance.AcquireLockWithTimeout(ctx, root, maintenance.DefaultLockName, 30*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = maintenance.ReleaseLock(lock) }()
	if !maintenance.IsUpgradeHeld(root) {
		return errors.New("config prepare requires an active upgrade fence")
	}
	for _, unit := range maintenance.DefaultWriterUnits {
		active, stateErr := activeBackupUnitAction(ctx, "state", unit)
		if stateErr != nil {
			return stateErr
		}
		if active {
			return fmt.Errorf("config prepare refused: writer %s is still active", unit)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.DevMode || cfg.Quarantined || cfg.WorkersHeld || cfg.DNSSecondaryOnly {
		return errors.New("config prepare refuses development, restore, held-worker or secondary-only configuration")
	}
	if cfg.DatabaseURLPlatform == "" {
		return errors.New("config prepare requires the platform database")
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()
	snapshot, err := reconcile.AssembleConfigContext(ctx, cfg, &reconcile.VersionedStore{StoreQuerier: store.New(pool), DB: pool.Pool}, cfg.PublicIPv4, cfg.Nameservers)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(reconcile.BuildApplyParams(snapshot))
	if err != nil {
		return err
	}
	status, err := agentd.PrepareOffline(ctx, cfg, raw)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	fmt.Println("Configuration prepared; upgrade fence retained. Protocol readiness and safe release are NOT established.")
	return nil
}
