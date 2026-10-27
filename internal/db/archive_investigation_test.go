package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestRemovedInvestigationMemberSeesNothing(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, user, entry int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('inv','Inv') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'I','inv-i','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('investigator@example.test','x') RETURNING id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO archive_entries(tenant_id,message_id,path,received_at) VALUES($1,'m','/tmp/m',now()) RETURNING id", tenant).Scan(&entry); err != nil {
		t.Fatal(err)
	}
	q := store.New(tx)
	id, err := q.PlatformCreateInvestigation(ctx, store.PlatformCreateInvestigationParams{TenantID: tenant, Title: "Case", Purpose: "customer request", CreatedBy: user, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.PlatformGrantInvestigation(ctx, store.PlatformGrantInvestigationParams{InvestigationID: id, TenantID: tenant, UserID: user}); err != nil {
		t.Fatal(err)
	}
	if err = q.PlatformAddInvestigationSource(ctx, store.PlatformAddInvestigationSourceParams{InvestigationID: id, TenantID: tenant, ID: entry, AddedBy: user}); err != nil {
		t.Fatal(err)
	}
	rows, err := q.PlatformListInvestigationSources(ctx, store.PlatformListInvestigationSourcesParams{TenantID: tenant, InvestigationID: id, UserID: user})
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	if err = q.PlatformRevokeInvestigation(ctx, store.PlatformRevokeInvestigationParams{TenantID: tenant, InvestigationID: id, UserID: user}); err != nil {
		t.Fatal(err)
	}
	rows, err = q.PlatformListInvestigationSources(ctx, store.PlatformListInvestigationSourcesParams{TenantID: tenant, InvestigationID: id, UserID: user})
	if err != nil || len(rows) != 0 {
		t.Fatalf("removed member rows=%v err=%v", rows, err)
	}
}
