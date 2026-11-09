// Package testdb owns disposable PostgreSQL fixtures for integration tests.
// The configured URL supplies cluster credentials;
// migrations and destructive fixtures always run in newly created databases.
package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
)

const bootstrapLock = int64(7750360020)

// bootstrapLockWait bounds how long a fixture waits for the cluster-wide
// bootstrap lock. The lock is a single cluster-wide rendezvous shared by
// every package and every fixture, so a parallel full-suite run queues on
// it. The wait is generous enough to outlast a queue of fixtures but short
// enough that a genuinely stuck lock fails the test with a clear message
// instead of consuming the whole package timeout.
const bootstrapLockWait = 3 * time.Minute

// bootstrapTimeout bounds the lock wait plus the bootstrap itself.
const bootstrapTimeout = bootstrapLockWait + 2*time.Minute

// testDatabaseURLEnv is the only environment variable that points the
// integration fixtures at a PostgreSQL cluster.
const testDatabaseURLEnv = "URMAIL_TEST_DATABASE_URL"

// requireDatabaseURLEnv opts a job into the mandatory-DSN rule: when it is
// set, a missing URMAIL_TEST_DATABASE_URL is a failure instead of a skip.
// The CI integration job sets it; the plain unit-test job does not, because
// it deliberately runs without a database.
const requireDatabaseURLEnv = "URMAIL_TEST_REQUIRE_DATABASE"

// lookalikeDatabaseURLEnvs are test-scoped variables that look like the
// fixture DSN but are not. A renamed variable used to skip every
// integration test silently, which turns a broken CI setup into a green
// run. Only test-scoped names belong here: production and development
// variables such as URMAIL_DATABASE_URL are set on purpose by
// scripts/dev.sh and the installer.
var lookalikeDatabaseURLEnvs = []string{
	"URMAIL_TEST_DSN",
	"URMAIL_TEST_DATABASE_DSN",
	"URMAIL_TEST_DATABASE_URI",
	"URMAIL_TEST_DB_URL",
	"URMAIL_TEST_POSTGRES_URL",
	"URMAIL_TEST_PG_URL",
}

// genericDatabaseURLEnvs are shared names that also point at the production
// or development database. They are only a mistake when the run insists on
// a fixture DSN, so they are reported there and not on a developer machine.
var genericDatabaseURLEnvs = []string{
	"DATABASE_URL",
	"URMAIL_DATABASE_URL",
}

// testDatabaseURL resolves the fixture DSN. It returns an error when the
// environment is wrong (a lookalike variable is set, or a job that requires
// the database has no variable) and an empty DSN without an error only for a
// local run that simply has no cluster configured.
func testDatabaseURL() (string, error) {
	dsn := os.Getenv(testDatabaseURLEnv)
	if dsn != "" {
		return dsn, nil
	}
	for _, name := range lookalikeDatabaseURLEnvs {
		if os.Getenv(name) != "" {
			return "", fmt.Errorf("%s is set but %s is not: integration fixtures need the DSN in %s (rename the variable)",
				name, testDatabaseURLEnv, testDatabaseURLEnv)
		}
	}
	if os.Getenv(requireDatabaseURLEnv) == "" {
		return "", nil
	}
	for _, name := range genericDatabaseURLEnvs {
		if os.Getenv(name) != "" {
			return "", fmt.Errorf("%s is set but %s is not: this run requires a fixture DSN in %s",
				name, testDatabaseURLEnv, testDatabaseURLEnv)
		}
	}
	return "", fmt.Errorf("%s is not set but %s is: integration fixtures are mandatory for this run",
		testDatabaseURLEnv, requireDatabaseURLEnv)
}

// resolveTestDatabaseURL returns the fixture DSN or fails the test with an
// actionable message. A missing variable is only a skip for a local run
// that has no cluster; a lookalike variable or a job that requires the
// database without the variable fails instead, so a renamed variable can
// never turn the integration suite into a no-op.
func resolveTestDatabaseURL(t testing.TB) string {
	t.Helper()
	dsn, err := testDatabaseURL()
	if err != nil {
		t.Fatal(err)
	}
	if dsn == "" {
		t.Skipf("%s is not set; skipping PostgreSQL integration", testDatabaseURLEnv)
	}
	return dsn
}

// RequireDatabaseURL returns the fixture DSN for a test outside this
// package. It applies the same guard as New: a lookalike variable or a
// job that requires the database fails the test instead of skipping it.
func RequireDatabaseURL(t testing.TB) string {
	t.Helper()
	return resolveTestDatabaseURL(t)
}

// unlockBootstrap releases the cluster-wide bootstrap lock. It uses its own
// short context so a bootstrap that ran out of time still releases the
// lock instead of leaking it into the next fixture.
func unlockBootstrap(admin *pgx.Conn, lock int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := admin.Exec(ctx, "SELECT pg_advisory_unlock($1)", lock)
	return err
}

// lockBootstrap takes the cluster-wide bootstrap lock. The lock is
// advisory and shared by every package and every fixture, so under a
// parallel full-suite run the queue can be long: wait for it with a
// bounded retry loop on a dedicated deadline instead of inheriting the
// short connection timeout, which used to abort healthy runs with
// "lock cluster bootstrap: context deadline exceeded".
func lockBootstrap(ctx context.Context, admin *pgx.Conn, lock int64, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	backoff := 100 * time.Millisecond
	for {
		var locked bool
		if err := admin.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lock).Scan(&locked); err != nil {
			return err
		}
		if locked {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cluster bootstrap lock still held by another fixture after %s", wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
}

// New creates and bootstraps an isolated database, without applying migrations.
// Cleanup drops only this fixture, after later-registered pool cleanups run.
func New(t testing.TB) string {
	t.Helper()
	dsn := resolveTestDatabaseURL(t)
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
	// while the roles changed by Bootstrap are cluster-wide. The lock wait and
	// the bootstrap itself get their own generous deadline: under a parallel
	// full-suite run the queue for this single cluster-wide lock is longer
	// than the connect/create budget above, and the old shared context
	// aborted healthy runs with "lock cluster bootstrap: context deadline
	// exceeded".
	bootstrapCtx, bootstrapCancel := context.WithTimeout(context.Background(), bootstrapTimeout)
	defer bootstrapCancel()
	if err = lockBootstrap(bootstrapCtx, admin, bootstrapLock, bootstrapLockWait); err != nil {
		t.Fatalf("lock cluster bootstrap: %v", err)
	}
	bootstrapErr := db.Bootstrap(bootstrapCtx, target.String())
	unlockErr := unlockBootstrap(admin, bootstrapLock)
	if bootstrapErr != nil {
		t.Fatalf("bootstrap owned integration database: %v", bootstrapErr)
	}
	if unlockErr != nil {
		t.Fatalf("unlock cluster bootstrap: %v", unlockErr)
	}
	return target.String()
}

// MigrateTo builds a historical fixture without depending on retired roles
// left behind by another test. Keep role creation and migration under the same
// cluster lock used by production Up, which may retire these roles again.
func MigrateTo(t testing.TB, dsn string, version uint) {
	t.Helper()
	target, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(target.Path, "/urmail_fixture_") {
		t.Fatal("historical migrations require an owned fixture database")
	}
	ctx := context.Background()
	maintenance := *target
	maintenance.Path = "/postgres"
	query := maintenance.Query()
	query.Del("role")
	maintenance.RawQuery = query.Encode()
	admin, err := pgx.Connect(ctx, maintenance.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	// Same cluster-wide rendezvous as New, with the same bounded wait: a
	// historical fixture must not hang forever on the lock.
	lockCtx, lockCancel := context.WithTimeout(ctx, bootstrapTimeout)
	defer lockCancel()
	if err = lockBootstrap(lockCtx, admin, bootstrapLock, bootstrapLockWait); err != nil {
		t.Fatalf("lock cluster bootstrap: %v", err)
	}
	defer func() { _ = unlockBootstrap(admin, bootstrapLock) }()
	// Historical schema 0042 predates retirement. These are fixture-only,
	// inert owners; current bootstrap and production permissions stay unchanged.
	if version >= 42 {
		if _, err = admin.Exec(ctx, `DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_roundcube') THEN
  CREATE ROLE urmail_roundcube NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
 END IF;
 IF NOT pg_has_role('urmail_migrate','urmail_roundcube','MEMBER') THEN
  GRANT urmail_roundcube TO urmail_migrate;
 END IF;
END $$`); err != nil {
			t.Fatal(err)
		}
	}
	if version >= 68 {
		if _, err = admin.Exec(ctx, `DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_nextcloud') THEN
  CREATE ROLE urmail_nextcloud NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
 END IF;
 IF NOT pg_has_role('urmail_migrate','urmail_nextcloud','MEMBER') THEN
  GRANT urmail_nextcloud TO urmail_migrate;
 END IF;
END $$`); err != nil {
			t.Fatal(err)
		}
	}
	source, err := iofs.New(db.FS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	params := target.Query()
	params.Set("role", "urmail_migrate")
	target.RawQuery = params.Encode()
	connection, err := sql.Open("pgx", target.String())
	if err != nil {
		t.Fatal(err)
	}
	driver, err := migratepgx.WithInstance(connection, &migratepgx.Config{})
	if err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	migration, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	defer func() {
		a, b := migration.Close()
		if a != nil || b != nil {
			t.Error(a, b)
		}
	}()
	if err = migration.Migrate(version); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatal(err)
	}
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
