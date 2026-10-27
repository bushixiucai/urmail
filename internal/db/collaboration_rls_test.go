package db_test

import (
	"context"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestCollaborationMailboxRLSFailClosed(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, domain, boxA, boxB int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('collab','Collab') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'A','collab-a','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'collab.example.test','active','tok',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@collab.example.test','x','active') RETURNING id", tenant, domain).Scan(&boxA); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'b','b@collab.example.test','x','active') RETURNING id", tenant, domain).Scan(&boxB); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'People'),($1,$3,'Other')", tenant, boxA, boxB); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	none, err := app.TenantTx(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = none.QueryRow(ctx, "SELECT count(*) FROM mailbox_addressbooks").Scan(&count); err != nil {
		t.Fatal(err)
	}
	_ = none.Rollback(ctx)
	if count != 0 {
		t.Fatalf("tenant context without mailbox saw %d books", count)
	}

	own, err := app.MailboxTx(ctx, tenant, boxA)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = own.Rollback(ctx) }()
	if err = own.QueryRow(ctx, "SELECT count(*) FROM mailbox_addressbooks").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("mailbox A saw %d books", count)
	}
	tag, err := own.Exec(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'Stolen')", tenant, boxB)
	if err == nil && tag.RowsAffected() > 0 {
		t.Fatal("mailbox A inserted a book for mailbox B")
	}
}

var _ = db.ErrNoMailbox
