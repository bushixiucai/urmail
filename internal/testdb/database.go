// Package testdb owns disposable PostgreSQL fixtures for integration tests.
// The configured URL supplies cluster credentials;
// migrations and destructive fixtures always run in newly created databases.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
)

const bootstrapLock = int64(7750360020)

// New creates and bootstraps an isolated database, without applying migrations.
// Cleanup drops only this fixture, after later-registered pool cleanups run.
func New(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("URMAIL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("URMAIL_TEST_DATABASE_URL is not set; skipping PostgreSQL integration")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL integration configuration")
	}
	if config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1" {
		t.Fatal("PostgreSQL integration fixtures require a loopback host")
	}
	target, err := url.Parse(dsn)
	if err != nil || target.Scheme != "postgres" && target.Scheme != "postgresql" {
		t.Fatal("PostgreSQL integration fixtures require a postgres URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// Cluster-wide roles need one lock rendezvous even when packages are given
	// different test database URLs. The maintenance database is never migrated.
	lockConfig := config.Copy()
	lockConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, lockConfig)
	if err != nil {
		t.Fatalf("connect to integration cluster: %v", err)
	}
	var suffix [12]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	name := "urmail_fixture_" + hex.EncodeToString(suffix[:])
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create owned integration database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop owned integration database: %v", err)
		}
		_ = admin.Close(cleanupCtx)
	})
	target.Path = "/" + name
	query := target.Query()
	// A dbname query argument can override a URL path in PostgreSQL clients.
	// Never carry that override into a fixture connection.
	query.Del("dbname")
	query.Del("database")
	target.RawQuery = query.Encode()
	ownedConfig, err := pgx.ParseConfig(target.String())
	if err != nil || ownedConfig.Database != name {
		t.Fatal("fixture URL does not target its owned database")
	}
	// Every package rendezvouses on postgres: advisory locks are database-local,
	// while the roles changed by Bootstrap are cluster-wide.
	if _, err = admin.Exec(ctx, "SELECT pg_advisory_lock($1)", bootstrapLock); err != nil {
		t.Fatalf("lock cluster bootstrap: %v", err)
	}
	bootstrapErr := db.Bootstrap(ctx, target.String())
	_, unlockErr := admin.Exec(ctx, "SELECT pg_advisory_unlock($1)", bootstrapLock)
	if bootstrapErr != nil {
		t.Fatalf("bootstrap owned integration database: %v", bootstrapErr)
	}
	if unlockErr != nil {
		t.Fatalf("unlock cluster bootstrap: %v", unlockErr)
	}
	return target.String()
}

// OpenMigrated creates one owned fixture and opens the two production roles.
func OpenMigrated(t testing.TB) (*db.Pool, *db.Pool) {
	t.Helper()
	dsn := New(t)
	target, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := target.Query()
	params.Set("role", "urmail_migrate")
	target.RawQuery = params.Encode()
	migrator, err := db.NewMigrator(target.String())
	if err != nil {
		t.Fatal(err)
	}
	if err = migrator.Up(context.Background()); err != nil {
		_ = migrator.Close()
		t.Fatal(err)
	}
	if err = migrator.Close(); err != nil {
		t.Fatal(err)
	}
	platform, err := db.OpenPlatform(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(platform.Close)
	app, err := db.OpenApp(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return platform, app
}
