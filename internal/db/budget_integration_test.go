package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
)

func TestBudgetPhysicalIsolationAndLifecycle(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()
	budget := db.Budget{MaxConns: 1, MaxQueue: 0, AcquireTimeout: 100 * time.Millisecond, OperationTimeout: time.Second}
	core, err := db.OpenBudgeted(ctx, url, db.RolePlatform, "test-core", budget)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	optional, err := db.OpenBudgeted(ctx, url, db.RolePlatform, "test-ai", budget)
	if err != nil {
		t.Fatal(err)
	}
	defer optional.Close()
	if core.Pool == optional.Pool {
		t.Fatal("shared physical pool")
	}
	tx, err := optional.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = optional.Exec(ctx, "SELECT 1"); !errors.Is(err, db.ErrBudgetExhausted) {
		t.Fatalf("exhaustion = %v", err)
	}
	if _, err = core.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatalf("core blocked by optional: %v", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := optional.Query(ctx, "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = optional.Exec(ctx, "SELECT 1"); !errors.Is(err, db.ErrBudgetExhausted) {
		t.Fatal(err)
	}
	rows.Close()
	var n int
	if err = optional.QueryRow(ctx, "SELECT 1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err = optional.QueryRow(ctx, "SELECT 1 WHERE false").Scan(&n); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err = optional.Exec(ctx, "SELECT pg_sleep(5)"); err == nil {
		t.Fatal("operation timeout missing")
	}
	if _, err = optional.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatal("failed operation leaked permit", err)
	}
	// Shutdown must reclaim even abandoned rows, not wait for their owner.
	rows, err = optional.Query(ctx, "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { optional.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on abandoned rows")
	}
	rows.Close()
	if _, err = optional.Exec(ctx, "SELECT 1"); !errors.Is(err, db.ErrPoolClosed) {
		t.Fatal(err)
	}
}
