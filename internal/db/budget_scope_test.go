package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
)

func TestBudgetScopeAndAbandonedTransaction(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()
	p, err := db.OpenBudgeted(ctx, url, db.RoleApp, "test-scope", db.Budget{MaxConns: 1, MaxQueue: 1, AcquireTimeout: time.Second, OperationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	tx, err := p.UserTx(ctx, 11, 22)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := db.CurrentTenant(ctx, tx); err != nil || id != 11 {
		t.Fatalf("tenant %d %v", id, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = p.UserTx(ctx, 33, 44)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := db.CurrentTenant(ctx, tx); err != nil || id != 33 {
		t.Fatalf("tenant leaked %d %v", id, err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var tenant, user int
	if err = p.QueryRow(ctx, "SELECT coalesce(nullif(current_setting('urmail.tenant_id',true),'')::int,0), coalesce(nullif(current_setting('urmail.user_id',true),'')::int,0)").Scan(&tenant, &user); err != nil || tenant != 0 || user != 0 {
		t.Fatalf("scope leaked: %d %d %v", tenant, user, err)
	}
	_, err = p.UserTx(ctx, 55, 66)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on abandoned transaction")
	}
}
