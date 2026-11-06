package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/jackc/pgx/v5"

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
	return writeDataCheck(ctx, pool, os.Stdout)
}

func writeDataCheck(ctx context.Context, pool *db.Pool, output io.Writer) error {
	// Count, bounded IDs and existing diagnostics describe one read-only snapshot.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	rows, err := q.PlatformDataIssues(ctx)
	if err != nil {
		return err
	}
	if err = json.NewEncoder(output).Encode(map[string]any{"reference_and_allocation_issues": rows, "changed": false}); err != nil {
		return err
	}
	if len(rows) > 0 {
		return fmt.Errorf("%d reference/allocation issues; no data changed", len(rows))
	}
	return nil
}
