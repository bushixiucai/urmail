package handlers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestCreateRefundReservesOnlySuccessfulPaymentBalance(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := context.Background()
	var brandID, tenantID, orderID, paymentID, userID int64
	steps := []struct {
		query string
		args  []any
		dest  *int64
	}{
		{query: "INSERT INTO brands(slug,name) VALUES('refund-reservation','Refund') RETURNING id", dest: &brandID},
		{query: "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Refund','refund-reservation','active') RETURNING id", args: []any{&brandID}, dest: &tenantID},
		{query: "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency) VALUES($1,'ORD-refund-reservation','new','paid',1000,'CNY') RETURNING id", args: []any{&tenantID}, dest: &orderID},
		{query: "INSERT INTO payments(tenant_id,order_id,provider,provider_txn_id,amount_cents,currency,status) VALUES($1,$2,'manual','refund-payment-1',1000,'CNY','succeeded') RETURNING id", args: []any{&tenantID, &orderID}, dest: &paymentID},
	}
	for _, step := range steps {
		args := make([]any, len(step.args))
		for i, a := range step.args {
			if p, ok := a.(*int64); ok {
				args[i] = *p
			} else {
				args[i] = a
			}
		}
		if err := platform.QueryRow(ctx, step.query, args...).Scan(step.dest); err != nil {
			t.Fatal(err)
		}
	}
	if err := platform.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('refund-owner@example.test','unused') RETURNING id").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", userID, tenantID); err != nil {
		t.Fatal(err)
	}
	create := func(refundNo string, amount int64, currency string) (store.Refunds, error) {
		tx, err := app.UserTx(ctx, tenantID, userID)
		if err != nil {
			return store.Refunds{}, err
		}
		defer tx.Rollback(ctx)
		result, err := store.New(tx).CreateRefund(ctx, store.CreateRefundParams{TenantID: tenantID, OrderID: orderID, PaymentID: paymentID, RefundNo: refundNo, AmountCents: amount, Currency: currency, Reason: "test"})
		if err != nil {
			return result, err
		}
		if err = tx.Commit(ctx); err != nil {
			return result, err
		}
		return result, nil
	}
	first, err := create("RF-reserve-1", 600, "CNY")
	if err != nil || first.Status != "pending" {
		t.Fatalf("first refund=%+v err=%v", first, err)
	}
	// The reservation routine raises distinct conditions instead of returning
	// an empty result, so the caller can tell over-balance from duplicate or
	// currency mismatch (M5 review 3.32).
	assertRefundRejected := func(t *testing.T, err error, want string) {
		t.Helper()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Message != want {
			t.Fatalf("refund rejected with %v, want %s", err, want)
		}
	}
	if _, err = create("RF-reserve-2", 401, "CNY"); err == nil {
		t.Fatal("over balance refund inserted")
	} else {
		assertRefundRejected(t, err, "REFUND_EXCEEDS_PAYMENT")
	}
	if _, err = create("RF-reserve-3", 400, "CNY"); err != nil {
		t.Fatalf("remaining balance refund: %v", err)
	}
	if _, err = create("RF-reserve-4", 1, "CNY"); err == nil {
		t.Fatal("refund beyond fully reserved payment inserted")
	} else {
		assertRefundRejected(t, err, "REFUND_EXCEEDS_PAYMENT")
	}
	var total int64
	if err = platform.QueryRow(ctx, "SELECT COALESCE(sum(amount_cents),0) FROM refunds WHERE tenant_id=$1 AND payment_id=$2", tenantID, paymentID).Scan(&total); err != nil || total != 1000 {
		t.Fatalf("reserved cents=%d err=%v", total, err)
	}
	if _, err = platform.Exec(ctx, "UPDATE refunds SET status='rejected' WHERE tenant_id=$1 AND payment_id=$2", tenantID, paymentID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := create(fmt.Sprintf("RF-concurrent-%d", i), 600, "CNY")
			outcomes <- e
		}(i)
	}
	wg.Wait()
	close(outcomes)
	success := 0
	for e := range outcomes {
		if e == nil {
			success++
			continue
		}
		// Losing racers raise the distinct over-balance condition instead of
		// returning an empty result (M5 review 3.32).
		assertRefundRejected(t, e, "REFUND_EXCEEDS_PAYMENT")
	}
	if success != 1 {
		t.Fatalf("expected exactly one concurrent reservation, got %d", success)
	}
	if err = platform.QueryRow(ctx, "SELECT COALESCE(sum(amount_cents),0) FROM refunds WHERE tenant_id=$1 AND payment_id=$2 AND status IN ('pending','approved','processing','completed')", tenantID, paymentID).Scan(&total); err != nil || total != 600 {
		t.Fatalf("outstanding total=%d err=%v", total, err)
	}
	if _, err = create("RF-wrong-currency", 1, "USD"); err == nil {
		t.Fatal("wrong currency refund accepted")
	} else {
		assertRefundRejected(t, err, "REFUND_CURRENCY_MISMATCH")
	}
	// RF-reserve-1 definitely committed, so reusing its number must hit the
	// duplicate condition regardless of which concurrent racer won.
	if _, err = create("RF-reserve-1", 500, "CNY"); err == nil {
		t.Fatal("duplicate refund number accepted")
	} else {
		assertRefundRejected(t, err, "REFUND_DUPLICATE")
	}
}
