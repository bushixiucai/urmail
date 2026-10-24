package db_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// legacyDomainDatabase deliberately stops at schema 31, so the public Up
// entry point must protect the existing data before historical migration 32.
func legacyDomainDatabase(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dsn := testdb.New(t)
	source, err := iofs.New(db.FS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	migrationURL, _ := url.Parse(dsn)
	params := migrationURL.Query()
	params.Set("role", "urmail_migrate")
	migrationURL.RawQuery = params.Encode()
	conn, err := sql.Open("pgx", migrationURL.String())
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
	connection, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return dsn, connection
}

func TestUpgradeProtectsHistoricalDomainsBeforeMigration32(t *testing.T) {
	dsn, connection := legacyDomainDatabase(t)
	ctx := context.Background()
	var brand, tenant, domain int64
	if err := connection.QueryRowContext(ctx, "INSERT INTO brands(slug,name) VALUES('upgrade-test','Upgrade') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRowContext(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Upgrade','upgrade','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRowContext(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,created_at) VALUES($1,'upgrade.example.test','active','legacy',now()-interval '30 days') RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	migration, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer migration.Close()
	if err = migration.Up(ctx); err == nil || !strings.Contains(err.Error(), "DOMAIN_VERIFICATION_PREFLIGHT") {
		t.Fatalf("expected protective preflight, got %v", err)
	}
	var status string
	var verified sql.NullTime
	if err = connection.QueryRowContext(ctx, "SELECT status,verified_at FROM domains WHERE id=$1", domain).Scan(&status, &verified); err != nil {
		t.Fatal(err)
	}
	if status != "active" || verified.Valid {
		t.Fatalf("preflight changed domain: %s %#v", status, verified)
	}
	version, dirty, err := migration.Version()
	if err != nil || version != 31 || dirty {
		t.Fatalf("preflight damaged migration state: %d %v %v", version, dirty, err)
	}
	// This represents the canonical write performed only after independent
	// proof by the compatibility CLI; no fabricated past timestamp is used.
	if _, err = connection.ExecContext(ctx, "UPDATE domains SET verified_at=statement_timestamp() WHERE id=$1", domain); err != nil {
		t.Fatal(err)
	}
	if err = migration.Up(ctx); err != nil {
		t.Fatalf("upgrade after real proof: %v", err)
	}
	if err = connection.QueryRowContext(ctx, "SELECT status FROM domains WHERE id=$1", domain).Scan(&status); err != nil || status != "active" {
		t.Fatalf("verified domain demoted: %s %v", status, err)
	}
	var guards int
	if err = connection.QueryRowContext(ctx, "SELECT count(*) FROM pg_constraint WHERE conname='urmail_upgrade_domain_verification_guard'").Scan(&guards); err != nil || guards != 0 {
		t.Fatalf("temporary migration guard leaked: %d %v", guards, err)
	}
}

func TestUpgradeRefusesDirtySchemaWithoutChangingBusinessRows(t *testing.T) {
	dsn, connection := legacyDomainDatabase(t)
	if _, err := connection.ExecContext(context.Background(), "UPDATE schema_migrations SET dirty=true"); err != nil {
		t.Fatal(err)
	}
	migration, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer migration.Close()
	if err = migration.Up(context.Background()); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty schema accepted: %v", err)
	}
}

func TestDomainChallengeRotationIsAtomicAndTenantScoped(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	migration, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer migration.Close()
	if err = migration.Up(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var brand, tenant, domain int64
	if err = pool.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('challenge','Challenge') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Challenge','challenge','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,created_at,verification_issued_at,verification_expires_at) VALUES($1,'challenge.example.test','pending_dns','old',now()-interval '1 year',now()-interval '8 days',now()-interval '1 day') RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	queries := store.New(pool.Pool)
	challenge, err := queries.RotateDomainChallenge(ctx, store.RotateDomainChallengeParams{TenantID: tenant, ID: domain, VerificationToken: "new-token"})
	if err != nil {
		t.Fatal(err)
	}
	if !challenge.VerificationExpiresAt.Time.After(time.Now()) {
		t.Fatal("renewal reused old domain expiry")
	}
	if _, err = queries.VerifyDomainChallenge(ctx, store.VerifyDomainChallengeParams{TenantID: tenant, ID: domain, VerificationToken: "old"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revoked token activated domain: %v", err)
	}
	if _, err = queries.VerifyDomainChallenge(ctx, store.VerifyDomainChallengeParams{TenantID: tenant + 1, ID: domain, VerificationToken: "new-token"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("other tenant activated domain: %v", err)
	}
	verified, err := queries.VerifyDomainChallenge(ctx, store.VerifyDomainChallengeParams{TenantID: tenant, ID: domain, VerificationToken: "new-token"})
	if err != nil || verified.Status != "active" || !verified.VerifiedAt.Valid {
		t.Fatalf("fresh proof failed: %+v %v", verified, err)
	}
	if _, err = queries.RotateDomainChallenge(ctx, store.RotateDomainChallengeParams{TenantID: tenant, ID: domain, VerificationToken: "third"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("active domain proof rotated: %v", err)
	}
}
