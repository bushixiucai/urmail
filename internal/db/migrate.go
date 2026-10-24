package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/bushixiucai/urmail/migrations"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	// Register the pgx driver for the migration library's database/sql adapter.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Migrator applies schema migrations.
type Migrator struct {
	migrate *migrate.Migrate
	sqlDB   *sql.DB
}

// NewMigrator builds a migrator over the embedded migrations and the given
// database. The database used for migrations is the platform connection: the
// tenant-scoped role cannot create or alter tables.
func NewMigrator(databaseURL string) (*Migrator, error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}

	sqlDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open migration database: %w", err)
	}

	driver, err := migratepgx.WithInstance(sqlDB, &migratepgx.Config{})
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("open migration database: %w", err)
	}

	instance, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("create migrator: %w", err)
	}
	return &Migrator{migrate: instance, sqlDB: sqlDB}, nil
}

// Up applies every pending migration.
func (m *Migrator) Up(ctx context.Context) (result error) {
	// This lock serializes every supported migration entry point, including
	// library users, restore, and tests. The migration engine keeps its own
	// independent lock; never hold a table lock across its connections.
	conn, err := m.sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock(7750360032)"); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(7750360032)") }()
	version, dirty, err := m.Version()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("schema version %d is dirty; diagnose the failed migration before upgrading", version)
	}
	// Reach the last schema before the destructive historical migration. A
	// fresh database has no domains to inspect until its tables exist.
	for version < 31 {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = m.migrate.Steps(1); err != nil {
			if errors.Is(err, migrate.ErrNoChange) {
				return nil
			}
			return fmt.Errorf("apply migration before verification preflight: %w", err)
		}
		version, dirty, err = m.Version()
		if err != nil {
			return err
		}
		if dirty {
			return fmt.Errorf("schema version %d became dirty during migration", version)
		}
	}
	guarded := false
	if version == 31 {
		if err = prepareDomainVerificationUpgrade(ctx, conn); err != nil {
			return err
		}
		guarded = true
	}
	if guarded {
		defer func() {
			_, cleanup := conn.ExecContext(context.Background(), "ALTER TABLE domains DROP CONSTRAINT IF EXISTS urmail_upgrade_domain_verification_guard")
			result = errors.Join(result, cleanup)
		}()
	}
	if err = m.migrate.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// prepareDomainVerificationUpgrade refuses to demote a historical domain.
// A temporary NOT VALID guard closes the preflight-to-0032 write race. It
// changes no business rows and is removed after the migration engine exits.
func prepareDomainVerificationUpgrade(ctx context.Context, conn *sql.Conn) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "LOCK TABLE domains IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,tenant_id,fqdn FROM domains WHERE status='active' AND (verified_at IS NULL OR verified_at > statement_timestamp()) ORDER BY tenant_id,id")
	if err != nil {
		return err
	}
	var issues []string
	for rows.Next() {
		var domainID, tenantID int64
		var fqdn string
		if err = rows.Scan(&domainID, &tenantID, &fqdn); err != nil {
			_ = rows.Close()
			return err
		}
		issues = append(issues, fmt.Sprintf("tenant=%d domain=%d fqdn=%s", tenantID, domainID, fqdn))
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if len(issues) > 0 {
		return fmt.Errorf("DOMAIN_VERIFICATION_PREFLIGHT: %d active domains need independent ownership proof before migration 0032; run urmailctl domain challenge/verify; no rows changed: %v", len(issues), issues)
	}
	if _, err = tx.ExecContext(ctx, "ALTER TABLE domains DROP CONSTRAINT IF EXISTS urmail_upgrade_domain_verification_guard"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "ALTER TABLE domains ADD CONSTRAINT urmail_upgrade_domain_verification_guard CHECK (status <> 'active' OR (verified_at IS NOT NULL AND verified_at <= statement_timestamp())) NOT VALID"); err != nil {
		return err
	}
	return tx.Commit()
}

// Down rolls back the most recent migration.
func (m *Migrator) Down(ctx context.Context) error {
	conn, err := m.sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock(7750360032)"); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(7750360032)") }()
	if err := m.migrate.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("roll back migration: %w", err)
	}
	return nil
}

// Version returns the current schema version and whether the database is
// dirty (a migration failed halfway).
func (m *Migrator) Version() (uint, bool, error) {
	version, dirty, err := m.migrate.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read schema version: %w", err)
	}
	return version, dirty, nil
}

// Close releases the migrator's resources.
func (m *Migrator) Close() error {
	sourceErr, databaseErr := m.migrate.Close()
	return errors.Join(sourceErr, databaseErr)
}

// FS exposes the embedded migrations for tooling and tests.
func FS() fs.FS { return migrations.FS }
