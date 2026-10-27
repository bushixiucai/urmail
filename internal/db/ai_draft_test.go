package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestStaleAIDraftDoesNotOverwrite(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, domain, mailbox int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('aid','AI') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'A','aid-a','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,verification_token,verified_at,status) VALUES($1,'aid.example.test','token',now(),'active') RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'a','a@aid.example.test','x',1,'active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	q := store.New(tx)
	row, err := q.PlatformInsertAIDraft(ctx, store.PlatformInsertAIDraftParams{TenantID: tenant, MailboxID: mailbox, Subject: "Hello", Body: "first"})
	if err != nil || row.Version != 1 {
		t.Fatal(err, row.Version)
	}
	updated, err := q.PlatformUpdateAIDraft(ctx, store.PlatformUpdateAIDraftParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID, Subject: "Hello", Body: "user", Version: 1})
	if err != nil || updated != 2 {
		t.Fatal(err, updated)
	}
	_, err = q.PlatformUpdateAIDraft(ctx, store.PlatformUpdateAIDraftParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID, Subject: "Hello", Body: "late", Version: 1})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
}
