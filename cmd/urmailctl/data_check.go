package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

func runDataCheck(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()
	rows, err := store.New(pool.Pool).PlatformDataIssues(ctx)
	if err != nil {
		return err
	}
	if err = json.NewEncoder(os.Stdout).Encode(rows); err != nil {
		return err
	}
	if len(rows) > 0 {
		return fmt.Errorf("%d data issues require explicit operator review; no data changed", len(rows))
	}
	return nil
}
