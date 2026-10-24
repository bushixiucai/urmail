package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestDeliveryEventDedupeIsTenantScopedAndReplaySafe(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := context.Background()
	var brandID int64
	if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('delivery-telemetry','Delivery') RETURNING id").Scan(&brandID); err != nil {
		t.Fatal(err)
	}
	tenantIDs := make([]int64, 2)
	for i := range tenantIDs {
		if err := platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,$2,$3,'active') RETURNING id", brandID, string(rune('A'+i)), string(rune('a'+i))).Scan(&tenantIDs[i]); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 10, 23, 14, 0, 1, 0, time.UTC)
	insert := func(tenantID int64) int64 {
		t.Helper()
		tx, err := app.TenantTx(ctx, tenantID)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		count, err := store.New(tx).RecordDeliveryEvent(ctx, store.RecordDeliveryEventParams{TenantID: pgtype.Int8{Int64: tenantID, Valid: true}, Instance: "postfix", QueueID: "QID-123", Sender: "sender@example.net", SenderDomain: "example.net", Recipient: pgtype.Text{String: "user@dest.test", Valid: true}, RecipientDomain: pgtype.Text{String: "dest.test", Valid: true}, Status: "sent", OccurredAt: pgtype.Timestamptz{Time: base, Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if n := insert(tenantIDs[0]); n != 1 {
		t.Fatalf("first insert rows=%d", n)
	}
	if n := insert(tenantIDs[0]); n != 0 {
		t.Fatalf("same tenant replay rows=%d", n)
	}
	if n := insert(tenantIDs[1]); n != 1 {
		t.Fatalf("cross tenant queue ID collision suppressed valid event, rows=%d", n)
	}
	var count int64
	if err := platform.QueryRow(ctx, "SELECT count(*) FROM delivery_events WHERE queue_id='QID-123'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("stored cross-tenant event count=%d err=%v", count, err)
	}
}
