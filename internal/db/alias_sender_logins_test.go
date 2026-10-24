package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/migrations"
)

func TestAliasSenderLoginsAreExplicitTenantBoundAndLive(t *testing.T) {
	f := newMailboxPasswordFixture(t)
	var domain int64
	if err := f.platform.QueryRow(t.Context(), "SELECT domain_id FROM mailboxes WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := f.platform.QueryRow(t.Context(), "INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,kind) VALUES($1,$2,'manual',$3,'alias') RETURNING id", f.tenant, domain, f.address).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rows, err := store.New(f.platform.Pool).PlatformListAliases(t.Context())
	if err != nil || len(rows) != 1 || len(rows[0].SenderLogins) != 0 {
		t.Fatalf("receiving alias implicitly grants send: %v", err)
	}
	for _, logins := range [][]string{{"other@password.example.test"}, {"self@password.example.test\nroot@other.test"}, {"SELF@password.example.test"}, {""}, {"self@password.example.test", ""}} {
		if _, err = f.platform.Exec(t.Context(), "UPDATE mailbox_aliases SET sender_logins=$3 WHERE tenant_id=$1 AND id=$2", f.tenant, id, logins); err == nil {
			t.Fatal("invalid sender identity accepted")
		}
	}
	if _, err = f.platform.Exec(t.Context(), "UPDATE mailbox_aliases SET sender_logins=$3 WHERE tenant_id=$1 AND id=$2", f.tenant, id, []string{f.address}); err != nil {
		t.Fatal(err)
	}
	rows, err = store.New(f.platform.Pool).PlatformListAliases(t.Context())
	if err != nil || len(rows) != 1 || len(rows[0].SenderLogins) != 1 || rows[0].SenderLogins[0] != f.address {
		t.Fatal("explicit sender grant lost", err)
	}
	down, err := migrations.FS.ReadFile("0069_alias_sender_logins.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(t.Context(), f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err = conn.Exec(t.Context(), string(down)); err == nil {
		t.Fatal("down erased sender grants")
	}
	if _, err = f.platform.Exec(t.Context(), "UPDATE mailboxes SET send_enabled=false WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox); err != nil {
		t.Fatal(err)
	}
	rows, err = store.New(f.platform.Pool).PlatformListAliases(t.Context())
	if err != nil || len(rows) != 1 || len(rows[0].SenderLogins) != 0 {
		t.Fatal("disabled mailbox retains alias send capability", err)
	}
	if _, err = f.platform.Exec(t.Context(), "UPDATE mailbox_aliases SET local_part='edited' WHERE tenant_id=$1 AND id=$2", f.tenant, id); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = f.platform.QueryRow(t.Context(), "SELECT cardinality(sender_logins) FROM mailbox_aliases WHERE tenant_id=$1 AND id=$2", f.tenant, id).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("routing edit retained an invisible sender grant", err)
	}
	if _, err = conn.Exec(t.Context(), string(down)); err != nil {
		t.Fatal(err)
	}
}
