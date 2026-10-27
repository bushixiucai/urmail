package testdb

import (
	"testing"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestContactSearchWildcardStaysLiteral(t *testing.T) {
	platform, _ := OpenMigrated(t)
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, mailbox, book int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('search','Search') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'S','search-s','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'search.test','active','tok',now()) RETURNING id", tenant).Scan(&book); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@search.test','x','active') RETURNING id", tenant, book).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'Contacts') RETURNING id", tenant, mailbox).Scan(&book); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mailbox_contacts(tenant_id,mailbox_id,addressbook_id,display_name,emails) VALUES($1,$2,$3,'Ada','[]'),($1,$2,$3,'Percent % Name','[]')", tenant, mailbox, book); err != nil {
		t.Fatal(err)
	}
	rows, err := store.New(tx).PlatformSearchContacts(ctx, store.PlatformSearchContactsParams{TenantID: tenant, MailboxID: mailbox, Query: "%"})
	if err != nil || len(rows) != 1 || rows[0].DisplayName != "Percent % Name" {
		t.Fatalf("%+v %v", rows, err)
	}
}
