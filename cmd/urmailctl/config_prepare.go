package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/maintenance"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// runConfigPrepare is deliberately offline: no daemon or protocol service is started.
func runConfigPrepare(ctx context.Context, args []string) error {
	firstBoot := false
	for _, arg := range args {
		if arg == "--first-boot" {
			firstBoot = true
			continue
		}
		return errors.New("usage: config prepare [--first-boot]")
	}
	if os.Geteuid() != 0 && !firstBoot {
		return errors.New("config prepare requires root")
	}
	root := currentMaintenanceRoot()
	if firstBoot {
		// First boot renders an empty installation; the upgrade path keeps its
		// fence and fresh-backup preflight untouched.
		if maintenance.IsUpgradeHeld(root) {
			return errors.New("first-boot render refused: an upgrade fence is active; use config prepare")
		}
	} else {
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
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.DevMode || cfg.Quarantined || cfg.WorkersHeld || cfg.DNSSecondaryOnly {
		return errors.New("config prepare refuses development, restore, held-worker or secondary-only configuration")
	}
	if firstBoot {
		pool, closePool, err := openPlatformStorePool(ctx, cfg)
		if err != nil {
			return err
		}
		defer closePool()
		raw, err := assembleApplyParams(ctx, cfg, pool)
		if err != nil {
			return err
		}
		status, err := agentd.PrepareOfflineFirstBoot(ctx, cfg, pool, raw)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(status)
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		fmt.Println("First-boot configuration prepared. Start the services and run setup to create the first tenant.")
		return nil
	}
	if cfg.DatabaseURLPlatform == "" {
		return errors.New("config prepare requires the platform database")
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()
	raw, err := assembleApplyParams(ctx, cfg, pool)
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

// openPlatformStorePool resolves the platform store for either engine. The
// returned closer releases every handle this call opened. Callers that record
// bookkeeping (backup runs, first-boot render) share it so the SQLite and
// PostgreSQL paths cannot drift apart.
func openPlatformStorePool(ctx context.Context, cfg *config.Config) (*db.Pool, func(), error) {
	if cfg.IsSQLite() {
		if strings.TrimSpace(cfg.SQLitePath) == "" {
			return nil, nil, errors.New("platform store requires URMAIL_SQLITE_PATH in SQLite mode")
		}
		sqliteDB, err := sqlitedb.OpenExisting(ctx, cfg.SQLitePath, sqlitedb.ExpectedSchemaVersion)
		if err != nil {
			return nil, nil, fmt.Errorf("open verified sqlite database: %w", err)
		}
		pool, err := db.NewSQLitePool(sqliteDB, isolation.ScopePlatform)
		if err != nil {
			_ = sqliteDB.Close()
			return nil, nil, fmt.Errorf("open platform sqlite pool: %w", err)
		}
		return pool, func() {
			pool.Close()
			_ = sqliteDB.Close()
		}, nil
	}
	if cfg.DatabaseURLPlatform == "" {
		return nil, nil, errors.New("platform store requires the platform database")
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return nil, nil, err
	}
	return pool, func() { pool.Close() }, nil
}

// assembleApplyParams builds the typed snapshot both entries render from.
func assembleApplyParams(ctx context.Context, cfg *config.Config, pool *db.Pool) (json.RawMessage, error) {
	snapshot, err := reconcile.AssembleConfigContext(ctx, cfg, &reconcile.VersionedStore{StoreQuerier: db.Queries(pool), DB: pool.Pool}, cfg.PublicIPv4, cfg.Nameservers)
	if err != nil {
		return nil, err
	}
	return json.Marshal(reconcile.BuildApplyParams(snapshot))
}
