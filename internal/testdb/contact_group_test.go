package testdb

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestDeletingContactGroupKeepsOtherMemberships(t *testing.T) {
	platform, _ := OpenMigrated(t)
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, domain, mailbox, book, contact, first, second int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('groups','Groups') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'G','groups-g','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'groups.test','active','tok',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@groups.test','x','active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'Contacts') RETURNING id", tenant, mailbox).Scan(&book); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contacts(tenant_id,mailbox_id,addressbook_id,display_name) VALUES($1,$2,$3,'Ada') RETURNING id", tenant, mailbox, book).Scan(&contact); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contact_groups(tenant_id,mailbox_id,addressbook_id,name) VALUES($1,$2,$3,'A') RETURNING id", tenant, mailbox, book).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contact_groups(tenant_id,mailbox_id,addressbook_id,name) VALUES($1,$2,$3,'B') RETURNING id", tenant, mailbox, book).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mailbox_contact_group_members(tenant_id,mailbox_id,group_id,contact_id) VALUES($1,$2,$3,$5),($1,$2,$4,$5)", tenant, mailbox, first, second, contact); err != nil {
		t.Fatal(err)
	}
	var removed bool
	if err = tx.QueryRow(ctx, "SELECT urmail_delete_contact_group($1,$2,$3)", tenant, mailbox, first).Scan(&removed); err != nil || !removed {
		t.Fatal(removed, err)
	}
	var remaining int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mailbox_contact_group_members WHERE tenant_id=$1 AND mailbox_id=$2 AND group_id=$3", tenant, mailbox, second).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatal(remaining, err)
	}
}

func TestContactCannotJoinAnotherAddressBookGroup(t *testing.T) {
	platform, _ := OpenMigrated(t)
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, domain, mailbox, firstBook, secondBook, contact, group int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('scope','Scope') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'S','scope-s','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'scope.test','active','tok',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@scope.test','x','active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'One') RETURNING id", tenant, mailbox).Scan(&firstBook); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'Two') RETURNING id", tenant, mailbox).Scan(&secondBook); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contacts(tenant_id,mailbox_id,addressbook_id,display_name) VALUES($1,$2,$3,'Ada') RETURNING id", tenant, mailbox, firstBook).Scan(&contact); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contact_groups(tenant_id,mailbox_id,addressbook_id,name) VALUES($1,$2,$3,'B') RETURNING id", tenant, mailbox, secondBook).Scan(&group); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mailbox_contact_group_members(tenant_id,mailbox_id,group_id,contact_id) VALUES($1,$2,$3,$4)", tenant, mailbox, group, contact); err == nil {
		t.Fatal("cross-book membership succeeded")
	}
}

func TestGroupMemberInsertRejectsAnotherAddressBook(t *testing.T) {
	platform, _ := OpenMigrated(t)
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, domain, mailbox, firstBook, secondBook, contact, group int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('insert','Insert') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'I','insert-i','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'insert.test','active','tok',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@insert.test','x','active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'One') RETURNING id", tenant, mailbox).Scan(&firstBook); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'Two') RETURNING id", tenant, mailbox).Scan(&secondBook); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contacts(tenant_id,mailbox_id,addressbook_id,display_name) VALUES($1,$2,$3,'Ada') RETURNING id", tenant, mailbox, firstBook).Scan(&contact); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contact_groups(tenant_id,mailbox_id,addressbook_id,name) VALUES($1,$2,$3,'B') RETURNING id", tenant, mailbox, secondBook).Scan(&group); err != nil {
		t.Fatal(err)
	}
	inserted, err := store.New(tx).PlatformInsertContactGroupMember(ctx, store.PlatformInsertContactGroupMemberParams{TenantID: tenant, MailboxID: mailbox, ID: group, ID_2: contact})
	if err != nil || inserted != 0 {
		t.Fatal(inserted, err)
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mailbox_contact_group_members WHERE tenant_id=$1", tenant).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestContactGroupDeleteQueryKeepsAnotherGroup(t *testing.T) {
	platform, _ := OpenMigrated(t)
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, domain, mailbox, book, first, second int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('deleteq','Delete') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'D','delete-d','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'delete.test','active','tok',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@delete.test','x','active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'Contacts') RETURNING id", tenant, mailbox).Scan(&book); err != nil {
		t.Fatal(err)
	}
	row, err := store.New(tx).PlatformInsertContactGroup(ctx, store.PlatformInsertContactGroupParams{TenantID: tenant, MailboxID: mailbox, ID: book, Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	first = row.ID
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contact_groups(tenant_id,mailbox_id,addressbook_id,name) VALUES($1,$2,$3,'B') RETURNING id", tenant, mailbox, book).Scan(&second); err != nil {
		t.Fatal(err)
	}
	removed, err := store.New(tx).PlatformDeleteContactGroup(ctx, store.PlatformDeleteContactGroupParams{PTenant: tenant, PMailbox: mailbox, PGroup: first})
	if err != nil || !removed {
		t.Fatal(removed, err)
	}
	var live int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mailbox_contact_groups WHERE tenant_id=$1 AND mailbox_id=$2 AND deleted_at IS NULL AND id=$3", tenant, mailbox, second).Scan(&live); err != nil || live != 1 {
		t.Fatal(live, err)
	}
}

func TestContactGroupInsertRejectsForeignAddressBook(t *testing.T) {
	platform, _ := OpenMigrated(t)
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, domain, mailbox, book int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('foreign','Foreign') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'F','foreign-f','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'foreign.test','active','tok',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@foreign.test','x','active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'One') RETURNING id", tenant, mailbox).Scan(&book); err != nil {
		t.Fatal(err)
	}
	_, err = store.New(tx).PlatformInsertContactGroup(ctx, store.PlatformInsertContactGroupParams{TenantID: tenant, MailboxID: mailbox + 1, ID: book, Name: "Hidden"})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
}

func TestDeletedContactGroupIsHiddenFromList(t *testing.T) {
	platform, _ := OpenMigrated(t)
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, domain, mailbox, book, group int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('listg','List') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'L','list-l','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'list.test','active','tok',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@list.test','x','active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'Contacts') RETURNING id", tenant, mailbox).Scan(&book); err != nil {
		t.Fatal(err)
	}
	row, err := store.New(tx).PlatformInsertContactGroup(ctx, store.PlatformInsertContactGroupParams{TenantID: tenant, MailboxID: mailbox, ID: book, Name: "Hidden"})
	if err != nil {
		t.Fatal(err)
	}
	group = row.ID
	if _, err = tx.Exec(ctx, "UPDATE mailbox_contact_groups SET deleted_at=now() WHERE id=$1", group); err != nil {
		t.Fatal(err)
	}
	rows, err := store.New(tx).PlatformListContactGroups(ctx, store.PlatformListContactGroupsParams{TenantID: tenant, MailboxID: mailbox})
	if err != nil || len(rows) != 0 {
		t.Fatalf("%d %v", len(rows), err)
	}
}

func TestDeletingContactRemovesGroupMembership(t *testing.T) {
	platform, _ := OpenMigrated(t)
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, domain, mailbox, book, contact, group int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('dropc','Drop') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'D','drop-d','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'drop.test','active','tok',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@drop.test','x','active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'Contacts') RETURNING id", tenant, mailbox).Scan(&book); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contacts(tenant_id,mailbox_id,addressbook_id,display_name) VALUES($1,$2,$3,'Ada') RETURNING id", tenant, mailbox, book).Scan(&contact); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contact_groups(tenant_id,mailbox_id,addressbook_id,name) VALUES($1,$2,$3,'A') RETURNING id", tenant, mailbox, book).Scan(&group); err != nil {
		t.Fatal(err)
	}
	var other, inserted int64
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contacts(tenant_id,mailbox_id,addressbook_id,display_name) VALUES($1,$2,$3,'Bea') RETURNING id", tenant, mailbox, book).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if inserted, err = store.New(tx).PlatformInsertContactGroupMember(ctx, store.PlatformInsertContactGroupMemberParams{TenantID: tenant, MailboxID: mailbox, ID: group, ID_2: contact}); err != nil || inserted != 1 {
		t.Fatal(inserted, err)
	}
	if inserted, err = store.New(tx).PlatformInsertContactGroupMember(ctx, store.PlatformInsertContactGroupMemberParams{TenantID: tenant, MailboxID: mailbox, ID: group, ID_2: other}); err != nil || inserted != 1 {
		t.Fatal(inserted, err)
	}
	if _, err = store.New(tx).PlatformDeleteContact(ctx, store.PlatformDeleteContactParams{TenantID: tenant, MailboxID: mailbox, ID: contact, Version: 1}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mailbox_contact_group_members WHERE contact_id=$1", contact).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mailbox_contact_group_members WHERE contact_id=$1", other).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
