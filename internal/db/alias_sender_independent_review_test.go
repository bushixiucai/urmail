package db_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestAliasSenderIndependentCrossTenantGuardsAndQueryDefense(t *testing.T) {
	f := newMailboxPasswordFixture(t)
	ctx := t.Context()
	var ownDomain, otherDomain, alias int64
	if err := f.platform.QueryRow(ctx, "SELECT domain_id FROM mailboxes WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox).Scan(&ownDomain); err != nil {
		t.Fatal(err)
	}
	if err := f.platform.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,provisioning_state) VALUES($1,'other-password.example.test','active','fixture',now(),'ready') RETURNING id", f.otherTenant).Scan(&otherDomain); err != nil {
		t.Fatal(err)
	}
	otherAddress := "other@other-password.example.test"
	if _, err := f.platform.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES($1,$2,'other',$3,'fixture',1024)", f.otherTenant, otherDomain, otherAddress); err != nil {
		t.Fatal(err)
	}
	if err := f.platform.QueryRow(ctx, "INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,kind,sender_logins) VALUES($1,$2,'manual',$3,'alias',$4) RETURNING id", f.tenant, ownDomain, f.address, []string{f.address}).Scan(&alias); err != nil {
		t.Fatal(err)
	}
	_, err := f.platform.Exec(ctx, "UPDATE mailbox_aliases SET sender_logins=$3 WHERE tenant_id=$1 AND id=$2", f.tenant, alias, []string{otherAddress})
	assertSenderGrantViolation(t, err)
	root, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close(context.Background()) }()
	tx, err := root.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SET LOCAL ROLE urmail_app"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SELECT set_config('urmail.tenant_id',$1,true),set_config('urmail.user_id',$2,true)", strconv.FormatInt(f.tenant, 10), strconv.FormatInt(f.user, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE mailbox_aliases SET sender_logins=$3 WHERE tenant_id=$1 AND id=$2", f.tenant, alias, []string{f.address}); err != nil {
		t.Fatal("valid own-tenant explicit grant denied", err)
	}
	_, err = tx.Exec(ctx, "UPDATE mailbox_aliases SET sender_logins=$3 WHERE tenant_id=$1 AND id=$2", f.tenant, alias, []string{otherAddress})
	assertSenderGrantViolation(t, err)
	_ = tx.Rollback(ctx)
	// Corrupt only this owned fixture inside a rollback-only transaction to prove
	// the platform query is an independent boundary even if stored data is bad.
	bad, err := f.platform.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bad.Rollback(ctx) }()
	if _, err = bad.Exec(ctx, "ALTER TABLE mailbox_aliases DISABLE TRIGGER alias_sender_logins_guard"); err != nil {
		t.Fatal(err)
	}
	if _, err = bad.Exec(ctx, "UPDATE mailbox_aliases SET sender_logins=$3 WHERE tenant_id=$1 AND id=$2", f.tenant, alias, []string{otherAddress}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.New(bad).PlatformListAliases(ctx)
	if err != nil || len(rows) != 1 || len(rows[0].SenderLogins) != 0 {
		t.Fatalf("query rendered a cross-tenant sender after fixture corruption: count=%d err=%v", len(rows), err)
	}
	if err = bad.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = f.platform.Exec(ctx, "UPDATE mailbox_aliases SET sender_logins=$3 WHERE tenant_id=$1 AND id=$2", f.tenant, alias, []string{otherAddress})
	assertSenderGrantViolation(t, err)
}

func assertSenderGrantViolation(t *testing.T, err error) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" || !strings.Contains(pg.Message, "ALIAS_SENDER_LOGIN_NOT_TENANT_MAILBOX") {
		t.Fatalf("cross-tenant grant was not denied by the authority guard: %v", err)
	}
}

func TestAliasSenderIndependentLiveFiltersRoutingResetAndDown(t *testing.T) {
	f := newMailboxPasswordFixture(t)
	ctx := t.Context()
	var domain, alias int64
	if err := f.platform.QueryRow(ctx, "SELECT domain_id FROM mailboxes WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err := f.platform.QueryRow(ctx, "INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,kind,sender_logins) VALUES($1,$2,'manual',$3,'alias',$4) RETURNING id", f.tenant, domain, f.address, []string{f.address}).Scan(&alias); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.platform.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	assertStored := func(want int) {
		t.Helper()
		var n int
		if err := f.platform.QueryRow(ctx, "SELECT cardinality(sender_logins) FROM mailbox_aliases WHERE tenant_id=$1 AND id=$2", f.tenant, alias).Scan(&n); err != nil || n != want {
			t.Fatalf("stored grants=%d want=%d err=%v", n, want, err)
		}
	}
	assertRendered := func(wantRows, wantGrants int) {
		t.Helper()
		rows, err := store.New(f.platform.Pool).PlatformListAliases(ctx)
		if err != nil || len(rows) != wantRows {
			t.Fatalf("renderable aliases=%d want=%d err=%v", len(rows), wantRows, err)
		}
		if wantRows > 0 && len(rows[0].SenderLogins) != wantGrants {
			t.Fatalf("rendered grants=%d want=%d", len(rows[0].SenderLogins), wantGrants)
		}
	}
	exec("UPDATE mailbox_aliases SET active=false WHERE tenant_id=$1 AND id=$2", f.tenant, alias)
	assertStored(1)
	assertRendered(0, 0)
	m, err := db.NewMigrator(f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	for {
		version, dirty, e := m.Version()
		if e != nil || dirty || version < 69 {
			t.Fatalf("invalid migration fixture v%d dirty=%t err=%v", version, dirty, e)
		}
		if version == 69 {
			break
		}
		if e = m.Down(ctx); e != nil {
			t.Fatal(e)
		}
	}
	if err = m.Down(ctx); err == nil || !strings.Contains(err.Error(), "ALIAS_SENDER_LOGIN_DOWNGRADE_REQUIRES_REVIEW") {
		t.Fatalf("inactive grant silently dropped on down69: %v", err)
	}
	assertStored(1)
	exec("UPDATE mailbox_aliases SET active=true WHERE tenant_id=$1 AND id=$2", f.tenant, alias)
	assertStored(1)
	assertRendered(1, 1)
	exec("UPDATE mailboxes SET send_enabled=false WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox)
	assertStored(1)
	assertRendered(1, 0)
	exec("UPDATE mailboxes SET send_enabled=true WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox)
	assertStored(1)
	assertRendered(1, 1)
	exec("UPDATE domains SET provisioning_state='pending' WHERE tenant_id=$1 AND id=$2", f.tenant, domain)
	assertStored(1)
	assertRendered(0, 0)
	exec("UPDATE domains SET provisioning_state='ready' WHERE tenant_id=$1 AND id=$2", f.tenant, domain)
	assertRendered(1, 1)
	exec("UPDATE mailbox_aliases SET target='external@example.test' WHERE tenant_id=$1 AND id=$2", f.tenant, alias)
	assertStored(0)
	assertRendered(1, 0)
	exec("UPDATE mailbox_aliases SET sender_logins=$3 WHERE tenant_id=$1 AND id=$2", f.tenant, alias, []string{f.address})
	assertStored(1)
	exec("UPDATE mailbox_aliases SET kind='forward' WHERE tenant_id=$1 AND id=$2", f.tenant, alias)
	assertStored(0)
}
