package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/migrations"
)

func TestLegacySieveBackendCannotBeForgedOrOverwritten(t *testing.T) {
	f := newMailboxPasswordFixture(t)
	ctx := t.Context()
	if _, err := f.platform.Exec(ctx, "INSERT INTO platform_admins(user_id) VALUES($1)", f.user); err != nil {
		t.Fatal(err)
	}
	q := store.New(f.platform)
	var domain int64
	if err := f.platform.QueryRow(ctx, "SELECT domain_id FROM mailboxes WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	id, err := q.PlatformCreateLegacyMailbox(ctx, store.PlatformCreateLegacyMailboxParams{TenantID: f.tenant, DomainID: domain, LocalPart: "legacy", Address: "legacy@password.example.test", PasswordHash: "synthetic-legacy-hash", QuotaBytes: -1})
	if err != nil {
		t.Fatal(err)
	}
	row, err := q.PlatformGetMailboxSieve(ctx, store.PlatformGetMailboxSieveParams{TenantID: f.tenant, ID: id})
	if err != nil || row.SieveMode != "legacy_v80" || row.SieveScript.Valid {
		t.Fatal("legacy backend facts invalid", err)
	}
	changed, err := q.PlatformSetMailboxSieve(ctx, store.PlatformSetMailboxSieveParams{TenantID: f.tenant, ID: id, SieveScript: pgtype.Text{String: "keep;", Valid: true}, ExpectedScript: ""})
	if err != nil || changed != 0 {
		t.Fatal("structured CAS overwrote legacy metadata", err)
	}
	for _, query := range []string{"UPDATE mailboxes SET sieve_script='keep;' WHERE tenant_id=$1 AND id=$2", "UPDATE mailboxes SET sieve_mode='managed' WHERE tenant_id=$1 AND id=$2"} {
		_, err = f.platform.Exec(ctx, query, f.tenant, id)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" {
			t.Fatal("unreviewed backend transition accepted", err)
		}
	}
	root, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close(context.Background()) }()
	tx, err := root.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SET LOCAL ROLE urmail_app"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SELECT set_config('urmail.tenant_id',$1,true),set_config('urmail.user_id',$2,true)", m8ID(f.tenant), m8ID(f.user)); err != nil {
		t.Fatal(err)
	}
	_, err = store.New(tx).PlatformCreateLegacyMailbox(ctx, store.PlatformCreateLegacyMailboxParams{TenantID: f.tenant, DomainID: domain, LocalPart: "forged", Address: "forged@password.example.test", PasswordHash: "synthetic-hash", QuotaBytes: -1})
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "42501" {
		t.Fatal("tenant forged privileged legacy backend", err)
	}
	_ = tx.Rollback(ctx)
	down, err := migrations.FS.ReadFile("0071_legacy_sieve_backend.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = root.Exec(ctx, string(down)); err == nil || !strings.Contains(err.Error(), "LEGACY_SIEVE_MAILBOXES_REQUIRE_CONTROLLED_ROLLBACK") {
		t.Fatal("downgrade erased legacy backend identity", err)
	}
}

func TestLegacySieveBackendCleanRollbackPreservesManagedRows(t *testing.T) {
	f := newMailboxPasswordFixture(t)
	root, err := pgx.Connect(t.Context(), f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close(context.Background()) }()
	for _, name := range []string{"0071_legacy_sieve_backend.down.sql", "0071_legacy_sieve_backend.up.sql"} {
		raw, e := migrations.FS.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = root.Exec(t.Context(), string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	var mode string
	if err = root.QueryRow(t.Context(), "SELECT sieve_mode FROM mailboxes WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox).Scan(&mode); err != nil || mode != "managed" {
		t.Fatal("managed backend changed on clean rollback", err)
	}
}
