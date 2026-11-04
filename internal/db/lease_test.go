package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
)

func TestBudgetLeaseLifecycleAndSessionLocks(t *testing.T) {
	for _, mode := range []string{"release", "cancel", "close", "rows", "transaction", "deadline", "executing", "close-rows", "close-transaction"} {
		t.Run(mode, func(t *testing.T) {
			url := testDatabaseURL(t)
			ctx := context.Background()
			p, err := db.OpenBudgeted(ctx, url, db.RolePlatform, "test-lease", db.Budget{MaxConns: 1, MaxQueue: 0, AcquireTimeout: time.Second, OperationTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			observer, err := db.OpenPlatform(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			request, cancel := context.WithCancel(ctx)
			defer cancel()
			lease, err := p.AcquireBudgeted(request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = lease.Exec(ctx, "SELECT pg_advisory_lock(192734)"); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if _, err = p.AcquireBudgeted(ctx); !errors.Is(err, db.ErrBudgetExhausted) || time.Since(start) > 100*time.Millisecond {
				t.Fatalf("second acquire %v", err)
			}
			// Transaction terminals retain lease ownership for the next transaction.
			tx, err := lease.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err = p.Exec(ctx, "SELECT 1"); !errors.Is(err, db.ErrBudgetExhausted) {
				t.Fatal("transaction released lease permit", err)
			}
			switch mode {
			case "release":
				lease.Release()
			case "cancel":
				cancel()
			case "close":
				p.Close()
			case "executing":
				executing := make(chan error, 1)
				go func() { _, queryErr := lease.Exec(ctx, "SELECT pg_sleep(5)"); executing <- queryErr }()
				cancel()
				select {
				case queryErr := <-executing:
					if queryErr == nil {
						t.Fatal("canceled statement succeeded")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("executing statement deadlocked")
				}
			case "close-rows":
				if _, err = lease.Query(ctx, "SELECT 1"); err != nil {
					t.Fatal(err)
				}
				p.Close()
			case "close-transaction":
				if _, err = lease.Begin(ctx); err != nil {
					t.Fatal(err)
				}
				p.Close()
			case "rows":
				if _, err = lease.Query(ctx, "SELECT 1"); err != nil {
					t.Fatal(err)
				}
				cancel()
			case "transaction":
				if _, err = lease.Begin(ctx); err != nil {
					t.Fatal(err)
				}
				cancel()
			case "deadline":
				select {
				case <-lease.Context().Done():
				case <-time.After(2 * time.Second):
					t.Fatal("no lease deadline")
				}
			}
			// Release joins any automatic cleanup and must remain safe when repeated.
			lease.Release()
			lease.Release()
			if _, err = lease.Exec(ctx, "SELECT 1"); !errors.Is(err, db.ErrPoolClosed) {
				t.Fatalf("closed lease usable: %v", err)
			}
			var locked bool
			// Disconnect cleanup is processed asynchronously by PostgreSQL after a
			// canceled statement. Require release within one second, not same-tick.
			lockDeadline := time.NewTimer(time.Second)
			retry := time.NewTicker(10 * time.Millisecond)
			defer lockDeadline.Stop()
			defer retry.Stop()
			for !locked {
				if err = observer.QueryRow(ctx, "SELECT pg_try_advisory_lock(192734)").Scan(&locked); err != nil {
					t.Fatal(err)
				}
				if locked {
					break
				}
				select {
				case <-retry.C:
				case <-lockDeadline.C:
					t.Fatal("session lock leaked after disconnect cleanup deadline")
				}
			}
			if _, err = observer.Exec(ctx, "SELECT pg_advisory_unlock_all()"); err != nil {
				t.Fatal(err)
			}
			if mode != "close" && mode != "close-rows" && mode != "close-transaction" {
				next, err := p.AcquireBudgeted(ctx)
				if err != nil {
					t.Fatal("permit leaked", err)
				}
				next.Release()
			}
		})
	}
}
