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
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Migrator applies schema migrations.
type Migrator struct {
	migrate *migrate.Migrate
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
	return &Migrator{migrate: instance}, nil
}

// Up applies every pending migration.
func (m *Migrator) Up(_ context.Context) error {
	if err := m.migrate.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// Down rolls back the most recent migration.
func (m *Migrator) Down(_ context.Context) error {
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
