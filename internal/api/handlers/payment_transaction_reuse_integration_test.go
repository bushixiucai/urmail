package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestProviderTransactionCannotSettleAnotherOrder(t *testing.T) {
	p, _ := authIntegrationDatabase(t)
	ctx := context.Background()
	var brandID, tenantID, orderA, orderB int64
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('txn-reuse','Transaction reuse') RETURNING id").Scan(&brandID); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Transaction reuse','txn-reuse','active') RETURNING id", brandID).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name   string
		target *int64
	}{
		{name: "TXN-REUSE-A", target: &orderA},
		{name: "TXN-REUSE-B", target: &orderB},
	} {
		if err := p.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency) VALUES($1,$2,'manual','pending',1000,'CNY') RETURNING id", tenantID, fixture.name).Scan(fixture.target); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := p.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.New(tx).CreatePayment(ctx, store.CreatePaymentParams{TenantID: tenantID, OrderID: pgtype.Int8{Int64: orderA, Valid: true}, Provider: payment.ProviderStripe, ProviderTxnID: pgtype.Text{String: "pi-txn-reuse", Valid: true}, AmountCents: 1000, Currency: "CNY", Status: payment.StatusSucceeded, Raw: []byte("{}")})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err = SettleOrderWithDetails(ctx, p, orderB, SettleOptions{Provider: payment.ProviderStripe, ProviderTxnID: "pi-txn-reuse", AmountCents: 1000, Currency: "CNY"})
	if !errors.Is(err, ErrPaymentVerificationFailed) {
		t.Fatalf("settlement error=%v, want ErrPaymentVerificationFailed", err)
	}
	var status string
	if err := p.QueryRow(ctx, "SELECT status FROM orders WHERE id=$1", orderB).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("second order status=%q, want pending", status)
	}
	var count int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM payments WHERE provider='stripe' AND provider_txn_id='pi-txn-reuse'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("payment rows=%d, want exactly one", count)
	}

	// Semantically identical JSONB event replays are idempotent; reused IDs with altered payloads fail.
	originalPayload := []byte(`{"id":"evt-replay","data":{"object":{"amount":1000}}}`)
	if err := SettleOrderWithDetails(ctx, p, orderA, SettleOptions{Provider: payment.ProviderStripe, ProviderTxnID: "pi-txn-reuse", EventID: "evt-replay", AmountCents: 1000, Currency: "CNY", RawPayload: originalPayload}); err != nil {
		t.Fatalf("first event settlement: %v", err)
	}
	if err := SettleOrderWithDetails(ctx, p, orderA, SettleOptions{Provider: payment.ProviderStripe, ProviderTxnID: "pi-txn-reuse", EventID: "evt-replay", AmountCents: 1000, Currency: "CNY", RawPayload: originalPayload}); err != nil {
		t.Fatalf("identical event replay: %v", err)
	}
	mutatedPayload := []byte(`{"id":"evt-replay","data":{"object":{"amount":9000}}}`)
	err = SettleOrderWithDetails(ctx, p, orderA, SettleOptions{Provider: payment.ProviderStripe, ProviderTxnID: "pi-txn-reuse", EventID: "evt-replay", AmountCents: 1000, Currency: "CNY", RawPayload: mutatedPayload})
	if !errors.Is(err, ErrPaymentVerificationFailed) {
		t.Fatalf("mutated event replay error=%v, want ErrPaymentVerificationFailed", err)
	}
}
