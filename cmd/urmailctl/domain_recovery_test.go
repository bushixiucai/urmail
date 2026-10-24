package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestRecoveryProofRejectsWrongIdentityAndExpiredCredential(t *testing.T) {
	now := time.Now().UTC()
	valid := recoveryProof{TenantID: 1, DomainID: 2, FQDN: "owned.example.test", Token: strings.Repeat("a", 64), IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	if err := valid.validate(now, 1, 2, valid.FQDN, valid.Token); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*recoveryProof)
	}{
		{"other tenant", func(p *recoveryProof) { p.TenantID = 3 }},
		{"other domain", func(p *recoveryProof) { p.DomainID = 9 }},
		{"reassigned name", func(p *recoveryProof) { p.FQDN = "other.example.test" }},
		{"rotated token", func(p *recoveryProof) { p.Token = strings.Repeat("b", 64) }},
		{"expired", func(p *recoveryProof) { p.ExpiresAt = now }},
		{"future", func(p *recoveryProof) { p.IssuedAt = now.Add(time.Hour) }},
		{"unbounded", func(p *recoveryProof) { p.ExpiresAt = now.Add(8 * 24 * time.Hour) }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := valid
			tt.change(&p)
			if err := p.validate(now, 1, 2, valid.FQDN, valid.Token); err == nil {
				t.Fatal("unsafe recovery credential accepted")
			}
		})
	}
}

// The public recovery command invokes this same helper after validating its
// root-owned credential file. A local resolver stand-in exercises proof and
// transaction boundaries without touching public DNS.
func TestRecoveryProofVerifiesBeforeCanonicalWrite(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	source, err := iofs.New(db.FS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := migratepgx.WithInstance(conn, &migratepgx.Config{})
	if err != nil {
		t.Fatal(err)
	}
	migration, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err = migration.Migrate(31); err != nil {
		t.Fatal(err)
	}
	if a, b := migration.Close(); a != nil || b != nil {
		t.Fatal(a, b)
	}
	pool, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var brand, tenant, domain int64
	if err = pool.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('recovery','Recovery') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Recovery','recovery','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	if err = pool.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,created_at) VALUES($1,'recovery.example.test','active',$2,now()-interval '30 days') RETURNING id", tenant, token).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	proof := recoveryProof{TenantID: tenant, DomainID: domain, FQDN: "recovery.example.test", Token: token, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	missing := func(context.Context, string) ([]string, error) { return []string{"not-owned"}, nil }
	if err = confirmDomainRecovery(ctx, pool, proof, missing); err == nil {
		t.Fatal("incorrect independent proof accepted")
	}
	var verified bool
	if err = pool.QueryRow(ctx, "SELECT verified_at IS NOT NULL FROM domains WHERE id=$1", domain).Scan(&verified); err != nil || verified {
		t.Fatalf("failed proof changed canonical verification: %v %v", verified, err)
	}
	valid := func(_ context.Context, name string) ([]string, error) {
		if name != "_urmail-verify."+proof.FQDN {
			t.Fatalf("wrong proof name %s", name)
		}
		return []string{token}, nil
	}
	if err = confirmDomainRecovery(ctx, pool, proof, valid); err != nil {
		t.Fatal(err)
	}
	var audits int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='domain.recovery.verified'", tenant).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("recovery audit %d %v", audits, err)
	}
	upgrader, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer upgrader.Close()
	if err = upgrader.Up(ctx); err != nil {
		t.Fatalf("proven old domain could not upgrade: %v", err)
	}
}
