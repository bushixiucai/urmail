package api_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// TestMailboxStoreRevocationAndBrandPG uses the real platform store against a
// synthetic PG database (no fakes): revoking one session rejects only that
// token, and brand ownership matches only the owning brand.
func TestMailboxStoreRevocationAndBrandPG(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	m, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	platform, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer platform.Close()
	for _, stmt := range []string{
		`INSERT INTO brands(id,slug,name) VALUES(1,'brand-a','A'),(2,'brand-b','B')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(1,1,'T1','t1','active')`,
		`INSERT INTO domains(id,tenant_id,fqdn,status,verification_token,verified_at,provisioning_state)
		 VALUES(1,1,'s7store.test','active','v',now(),'ready')`,
		`INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash,quota_bytes)
		 VALUES(1,1,1,'a','a@s7store.test','x',1024)`,
	} {
		if _, err = platform.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	token1, hash1, err := mailboxprincipal.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	token2, hash2, err := mailboxprincipal.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{hash1, hash2} {
		if _, err = platform.Exec(ctx, `INSERT INTO mailbox_sessions(token_hash,tenant_id,mailbox_id,password_version,expires_at,origin_host)
			VALUES($1,1,1,md5('x'),$2,'')`, hash, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	q := store.New(platform)
	s := handlers.StoreMailboxNative{Q: q}
	if _, err = s.GetMailboxSession(ctx, hash1); err != nil {
		t.Fatalf("first session lookup: %v", err)
	}
	if err = s.Revoke(ctx, hash1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetMailboxSession(ctx, hash1); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revoked token accepted: %v", err)
	}
	if _, err = s.GetMailboxSession(ctx, hash2); err != nil {
		t.Fatalf("other session affected by revocation: %v", err)
	}
	if !s.BrandTenantMatches(ctx, 1, 1) {
		t.Fatal("owning brand rejected")
	}
	if s.BrandTenantMatches(ctx, 1, 2) || s.BrandTenantMatches(ctx, 999, 1) {
		t.Fatal("foreign/missing brand accepted")
	}
	_ = token1
	_ = token2
}
